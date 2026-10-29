# A stronger approval for cards that widen trust or replace code

Design yardr-2nb1, proposed 2026-10-29. A recommendation, not a decision:
how an operator approves trust is the operator's to choose, so this
document names the pick it recommends and what each other candidate
would cost, and the choice itself goes to mckean as an ask once this
lands. No build bead is filed by this document; its last section lists
the beads the recommended pick needs, ready to file for whichever
candidate is chosen. Nothing here is built.

A *card* is an ask a person answers by approving rather than by picking
words: a run ask (`Ask.Run`, one yardr command), a setup ask
(`Ask.Setup`, a pack's whole setup), a secret ask (`Ask.Secret`, a value
typed) and a mapping ask (`Ask.Identity`). A card is *weighty* where
approving it widens what reaches outside the yard, whose word or
credential counts as a person's, or what runs inside the yard (design
yardr-ekmx; `internal/store/weight.go`), and *routine* otherwise. A
*device* is a program of a user that calls the yard from outside it,
let in by a token the yard gave it; footplate paired with a yard is
one. A *paired device of the operator* is such a device whose user is
the yard's operator. The *edge* is the hosted relay a yard keeps one
outbound link to, the *hosted web* is the browser view of a hosted yard
behind its gateway, and a *hosted yard* is one the service made and
runs (`hosted` setting true), whose operator has no shell on the
machine it lives on. docs/glossary.md has the rest of yardr's words.
In this document a *strong card* is a card the stronger approval of
section 5 is demanded of, and a *fresh sign-in* is a re-authentication
of the operator's hosted account, proved by the time the gateway last
authenticated them and not by the age of a cookie or a token.

## 1. The question

Five cards wait on this design. Each would let an operator with no
terminal approve, from a card, a command that today only a terminal
may give:

| bead | the card | its weight |
|---|---|---|
| yardr-r02z | `peer add <name> <invite>` | trust |
| yardr-tj0j | `peer set <name> --url <url>` | trust |
| yardr-tegt | `peer allow <peer> --expose/--trust/--reach` widening | trust |
| yardr-cpq7 | `source add <file>` replacing a standing source | code, and reads or outside where the replacement brings them |
| yardr-1bas | `pack apply <name>` first install of an embedded depot pack to a named depot | code, and credential, outside or trust where the pack brings them |

A sixth, yardr-esmj (`peer rm`, routine: it takes standing away), is
built now and waits on nothing here. Mckean's decision on ask 16305
(29 Oct) held the five and built the removal, because of the promise
docs/users.md makes today in "A stolen device of the operator can have
the commands of a run ask run, and no other" (docs/users.md:809) and
"What it still cannot have run" (docs/users.md:850): no run ask may
add a link, trust a name, move or remove a link, give it reach or
expose this yard to it, or replace a source from a file, *so it cannot
make itself a second way in — no new device, no new credential, and
the operator stands — and removing the device ends what it could do*.

The weighty gate does not keep that promise against theft. A weighty
approval takes the client's word (`weighed`) that its person saw the
weight and passed the client's gate — on footplate, the Mac's unlock;
in a terminal, the ask's number typed back (`internal/store/weight.go`
`weighedRefusal`, `internal/cli/weighed.go`). The yard checks what the
client says and verifies nothing about the person (docs/api.md,
"Weighty cards"). Whoever holds a paired operator device holds the
token the gate's word rides on, and the stolen device approves weighty
cards too: it writes mail as the operator, the yardmaster does what
the operator asks by mail, so the holder can have a card proposed and
then approve it. For a routine card that is the accepted bargain. For
the five it is not, because what they make outlives the device: a link
once added or trusted, a source once replaced, a pack once installed
all stand after `yardr user device rm`, and each is a way back in that
removing the device does not end.

So the question is narrow: when a card's approval would widen trust
or replace code, what must the approval prove *besides* possession of
a paired operator device, so that the five can be built and the
sentence "removing the device ends what it could do" stays true of
everything a stolen device can have run?

## 2. What exists today

### 2.1 What an approval proves today

An approval is one of four calls, each weighed the same way. A plain
or mapping pick is `AnswerAsk` (`internal/store/decisions.go:828`,
its `weighed` flag); a run ask is `ApproveAsk`, a setup ask
`ApproveSetup`, a secret ask `GiveSecret` (all in `OperatorCalls`,
`internal/rpc/api.go:147`). Inside the write, each weighs the card
again from the yard's records, refuses as stale where the weight is
not the one the card showed (`weightStale`), and refuses a weighty
card the call did not say was weighed (`weighedRefusal`,
`internal/store/weight.go:142`). `ruleAnswerAsk`
(`internal/store/policy.go:948`) and its kin hold an answer to the
person asked and to an operator. That is the whole proof: a request
the yard's record of a device vouches for (or a shell on the yard's
machine outside an agent's pane), an actor the policy allows, a card
unchanged since it was shown, and a client's word that a person saw
its weight. The phone's or Mac's unlock stands in front of the tap,
and the yard cannot verify it (docs/users.md:882, "Approvals cross
the edge").

The six weight classes (`internal/store/weight.go:29`) are
`credential`, `as-user`, `outside`, `code`, `trust` and `reads`.
Which rows carry them is the yard's, weighed when the card is written
and again when it is approved, never from the asker's words. A card
that narrows — a revoke, a lower limit, `user rm`, `timer rm`, a kind
taken off a link — carries none.

### 2.2 Where an approval may come from

Three ways in, and they are not equal (design yardr-x3g8 section 2.3,
as built). On the yard's socket, a process that can open it is trusted
as every party of the yard, and the store can tell an agent's pane
from a person's terminal by the process at the socket's other end. On
the listener, a device's token resolves to its user and the
call is marked `via <device>` (`internal/store/via.go`); the listener
serves `OperatorCalls` to a device of the operator and answers every
other caller as for a method the yard does not have. Down the link
from the edge, a paired device approves as on the listener, and the
edge sees the token on every call. Over a peer link, no approval
crosses at all: an answer that comes home can put a card aside or
decline it, never approve it, and `ApproveAsk` refuses a `bead@peer`
in its own words.

The browser view is a fourth surface that today approves almost
nothing weighty. Behind a gateway (`internal/web/jwt.go`) it verifies
the gateway's signed token on each request — issuer, audience,
signature against the fetched keys, expiry with a minute's leeway —
maps its email to a yard user through `web_jwt_users`, and acts as
that user (`RequestUser`, `RequestExpiry`). Its bead page answers
plain asks, puts them aside, declines them, and gives a secret with
`weighed` left false (`internal/web/acts.go` `beadGiveSecret`), so the
yard refuses every weighty giving there and names footplate or a
terminal. It has no run-ask approval at all: a card's line names the
`yardr bead answer … --approve` command. What the verifier keeps of a
token is the user and its expiry; when the gateway last authenticated
the person (its `auth_time`) is read by nobody.

### 2.3 The one second proof the yard already takes

A secret ask already demands more than a tap for one form. When the
yard leads a harness's login for the operator, a paired device gives
the code the sign-in page shows, down the link as from the yard's own
machine (docs/users.md:891; `AskSecret.OperatorLoginInput` in
`internal/store/secretask.go`). The code is used once, within minutes,
handed to the waiting login in memory and kept nowhere. Two things in
that pattern matter here. The proof is a value from a *second place*
— the sign-in page the yard is leading, not the device that answers —
so holding the answering device alone does not supply it. And the
yard takes it only for the operator, only down paths it names, and
refuses it everywhere else; a second proof in yardr has so far always
been scoped to one kind of card, never a general login. The earlier
approval design weighed a login, password or passkey second step and
left it open (design yardr-x3g8 section 5, question 4, and section 4,
"A login, a password or a passkey in yardr"). No passkey or WebAuthn
code stands in the tree: `grep -rni passkey internal/` finds only
that design.

### 2.4 The delay machinery that already stands

An ask put aside with Later leaves the queue for 24 hours or until
the asker's next note (`LaterFor`, `ReturnAt` in
`internal/store/asks.go`). Any ask can be taken back by its asker or
the operator (`TakeBackAsk`, `ruleTakeBackAsk`). An answer is written
to the bead and appears in its card and the event stream. That does
not guarantee an alert on every paired device. The ask's outside push
is queued when the ask is written and is dropped if it is answered
or put aside before delivery; other outside notifications depend on the user's
subscriptions, `notify` source and `tell` settings, and that source
need not be mail. A device's last use and address are listed
(`yardr user device list`), and removing a device refuses its token at
the next request and closes its streams. The existing queue and event
stream are reusable parts, but the delay candidate's promised notice
to every paired device and by mail would need new delivery work as
well as a state between approved and run and a cancelling answer.

## 3. The threat, stated exactly

One paired device of the operator is stolen and unlocked, or its
device token is taken without the device. The thief is patient: they
may wait hours or days, approve at a quiet hour, and use the yard at
leisure until the theft is noticed. The thief can do everything that
device can do. They read what an operator's device reads, write mail
as the operator, answer and approve any open card the device is served
(including sending `weighed`, since the yard takes the client's word),
ask the yardmaster by mail to propose a command and approve the card
that comes back, give a secret where a secret ask stands open, and
read the whole grant graph. They need not wait for a card to exist;
they can have one proposed.

Everything else stands. The yard's other paired devices, where the
operator has any, are in the operator's hands. The operator's accounts
are uncompromised: the hosted account's password, passkey and mailbox,
and any second factor on them, are the operator's alone, and a fresh
sign-in to that account cannot be completed with the device token or
with anything stored for the yard on the device. The yard host itself
is not stolen: its socket, its store and its operator's shell are
uncompromised, so an approval made at a terminal on the yard's machine
is still the operator's. No other yard is in the thief's hands, and no
person is coerced: the operator, if they act, acts against the thief.

Against that statement, a stronger approval of a strong card must
prove one of two things, with the device token alone insufficient for
both:

- **a second possession**: something the thief does not hold — a fresh
  authentication of the operator's account, a second paired device, or
  a passkey authenticator — proved independently of the stolen device
  token. For the recommended session-age proof, the gateway must
  authenticate after the operator opens this card's approval, and that
  one-use proof must return to this card; a merely recent standing
  session does not say who is approving now; or
- **a witnessed wait**: that the approval was announced at once on
  channels the thief cannot suppress — every other paired device and
  the operator's mail outside the yard — and stood uncancelled for a
  named time any of those channels could have cancelled in, so that
  silence is the operator's, not the thief's privacy.

It need not prove anything against a stolen yard host (whoever holds
the host holds the socket, and yardr authenticates nobody there:
docs/users.md:207), against a compromised operator account (that
account is the root the hosted yard's operator is reached by at all;
its loss is answered by the service's account recovery and by
`yardr user device rm`, not by a card), against a coerced operator,
or against the operator themselves. It need not hide that a card
exists: the thief can read the queue already. And it must not make
removal weaker: taking a way in back (`peer rm`, a revoke, a lower
limit, device removal itself) must stay at least as easy with the
stronger approval in place as without it, since the hour a theft is
noticed is the hour those are run.

Two consequences follow before any candidate is weighed. First, the
client's gate alone can never be the stronger approval: footplate's
unlock, the terminal's typed number, any ritual on the stolen device
is performed by the thief as readily as by the operator, which is the
bead's own observation that a weighty card does not help against
theft. What is wanted is not a heavier tap but a different witness.
Second, whatever is built must fail closed for the operator it cannot
witness: a yard that cannot produce the second proof approves no
strong card by device, and says where it can be approved, rather than
falling back to the tap it set out to replace.

## 4. The candidates

Each candidate is weighed against section 3 alone: what it proves,
what it costs an operator of a hosted yard (no terminal; the browser
behind the gateway and a paired device are their surfaces), and what
it costs an operator who runs their own yard (a terminal on the yard's
machine, a paired device for reading away from it). The cost that
matters is not only the build. It is what the operator must have, and
must do, on the day a link or a source has to change.

### 4.1 A fresh sign-in on the hosted web at approval time

The approval is made in the hosted web after the operator opens that
card's approval control. Each strong card starts a fresh gateway
authentication, even when the browser already has a recent session;
the window — ten minutes is the window this document recommends —
limits how long the returned proof can wait before use. The web holds
a one-use state for the card and returns to it after the gateway
authenticates now, rather than accepting its standing session. The
yard reads *when the gateway last authenticated the person*, not when
the page was loaded and not how old the device token is: the
gateway's token carries that time, the yard's verifier keeps it beside
the user and expiry it keeps today, and a token with no such time, a
time before this card's challenge, or one older than the window
approves no strong card. The device may read the card, put it aside
and decline it as today; it may not approve it.

What it proves is the second possession, in its account form: within
the last minutes, someone presented the operator account's own
credential — its password and second factor, or its passkey — to the
gateway, and the gateway's signed word for that, mapped through
`web_jwt_users` to the operator, is what the yard takes. The device
token is not presented and would not suffice. Under section 3 the
thief cannot mint that word: the account is uncompromised, and an
unlocked phone does not carry the account's password. If the operator
uses a passkey on that same phone, the gateway must require fresh user
verification for the sign-in rather than treating the already-unlocked
screen or an existing web session as a new authentication.

This proves the account was authenticated for this card, not that the
person read every line or that a person cannot take over during the
short return-to-card interaction. An already-fresh hosted web session
on the stolen device does not count: the gateway must authenticate
after the card's challenge, and the one-use state must return to and be
consumed by that card. The 10-minute window is for completing that one
approval, not for reusing an authentication across cards.

For a hosted operator the cost is the smallest of the four. They
already have the account and the sign-in; the work is the yard
keeping the authentication time, a run- and setup-approval surface in
the web where today there is none (section 2.2), one sign-in round
trip per strong card, and the matching change at the edge that asks
its gateway to re-authenticate even when a session is recent. No new
credential is enrolled, stored or recovered
by the yard, and the operator's one paired device is no limit: the
second possession is their account, which they have whether they have
one device or five. For a self-hosted operator there is no hosted
account and usually no gateway, so this candidate simply has nothing
to read — which is why section 5 gives that operator the terminal
instead of a weaker copy of this. A self-hosted operator who reads
away from the machine and has no gateway cannot approve a strong card
away from it under this candidate. That is a real narrowing, stated
in section 5.4 rather than papered over.

### 4.2 A passkey second factor on the operator's account

The operator enrols a passkey (a WebAuthn credential) for the yard,
and approving a strong card takes a fresh assertion over a challenge
the yard binds to the yard, the bead, the ask and the command as
shown, with user verification required. The yard verifies the
assertion against the enrolled public key and runs the command in the
same write as every approval.

What it proves is the strongest form of the second possession:
possession of the authenticator and its verification, at this moment,
for this card, unusable for another. A stolen unlocked device does
not satisfy it where the passkey asks for face, finger or PIN again,
and a taken token satisfies it nowhere.

The cost is a new identity system, and it lands on both kinds of
operator alike. The yard authenticates nobody today; there is no
WebAuthn code, no enrolment, no credential store, no recovery when
the authenticator is lost, and no answer for which origin a self-hosted
yard's passkey is bound to across its own listener, the edge and a
terminal. Every approving client — footplate, the web, `yardr bead
answer`, `yardr chat` — needs a WebAuthn path, and the terminal, whose
shell is the proof it already has, gains a ceremony it does not need.
A hosted operator gains a second account beside the one the service
already authenticates them with, and its recovery becomes the yard's
problem on the worst day. **The one fact that kills it: the gateway
already authenticates the hosted operator, so this builds yardr's
first authentication system — enrolment, storage, recovery and four
clients — to prove on a hosted yard what a fresh sign-in proves by
reading a time the gateway already signs, and on a self-hosted yard
what the terminal already proves for free.**

### 4.3 A delay, with notice to every device and by mail, and any device able to cancel

Approving a strong card starts it rather than running it. The yard
records the approval, tells every paired device of the operator and
the operator's address outside the yard at once, and runs the command
only when a named delay — hours, not minutes — has passed with no
cancellation. Any paired device, and a reply by mail, cancels in one
answer; the asker is told it is waiting and when it will run; the
card shows the wait wherever the card is shown. The machinery is
mostly standing (section 2.4): Later's return, TakeBack's refusal
shape, the notify path, and one new state between approved and run
that the run, the stale check and the one-run-once rule all respect.

What it proves is the witnessed wait, and nothing else. It does not
prove a second possession at all: the thief approves as validly as
the operator, and the approval runs unless somebody stops it. The
existing event stream and optional outside notifications do not meet
this candidate's promise of prompt notice to every paired device and
by mail; that delivery would have to be built.

For a hosted operator the cost is time on exactly the work that
cannot wait. Linking a peer and replacing a source are day-one and
repair work — a moved peer strands its link (yardr-tj0j's own words) —
and a delay of hours strands it further, while a delay short enough
to work through is short enough to sleep through. For a self-hosted
operator the cost is the same wait where a terminal approval was
immediate. Both also need a new delivery mechanism that reaches a
channel the thief cannot suppress. It would tell every paired device,
including the stolen one the thief can dismiss, and mail an address
that may be read on that same device. **The one fact that kills it:
the thief is patient by the terms of the threat, and the operator with
exactly one paired device has no independent channel guaranteed to
witness the wait, so the delay expires uncancelled in precisely the
theft it was built for, and the link it makes then outlives the device
as before.**
A delay is a good tell and a bad proof; section 5 keeps the audit
record and optional notifications, not its mandatory notice or wait.

### 4.4 A code from a second paired device — the yard's own secret-ask pattern

Approving a strong card on one device takes a short code shown on —
or confirmed by — a second paired device of the operator, as a
harness login's code is given by a paired device today (section 2.3).
The approving device presents the code with the approval; the yard
takes it once, within minutes, keeps it nowhere, and runs the command
only with it. The two devices are enrolled as today, by pairing.

What it proves is the second possession in its device form: two
independent device tokens of the operator, held at once. The thief
with one device, or one token, cannot supply it, however patient
they are, and unlike the delay it does not ask the operator to notice
anything.

For a hosted operator the cost is a second device they may not have.
The hosted operator this whole question exists for is, in the common
case, a person with one phone paired and no terminal; demanding a
second paired device tells them the five cards are not for them until
they enrol hardware the yard otherwise never asks for, and enrolling
it after the first device is stolen is pairing — itself an operator's
act the stolen device must not be able to start. For a self-hosted
operator with a phone and a laptop paired, the cost is small and the
proof is real, which is what makes this the strongest of the rejected
candidates rather than a weak one. **The one fact that kills it: a
yard whose operator has exactly one paired device — the hosted yard's
own starting shape — can never approve a strong card under it, so the
five cards it exists to unlock stay locked for the operator who needs
them most.** Section 5.5 names what that operator does instead under
the pick, and decision question 3 in section 8 keeps this candidate
whole for mckean: if a second device is ever required, it should be
required knowingly, not as the side effect of a code.

In one view:

| candidate | what it proves | hosted operator | self-hosted operator | settled by |
|---|---|---|---|---|
| fresh sign-in (4.1) | account re-authenticated within minutes | signs in again in the browser; account already held | nothing to read; terminal instead | recommended: no new credential, works with one device |
| passkey (4.2) | authenticator held, for this card | a second account to enrol and recover | yard becomes an authenticator service | the gateway already authenticates the hosted operator |
| delay + notice + cancel (4.3) | nobody objected in the window | waits hours on day-one work | waits where a terminal was immediate | patient thief, one-device operator, no witnessed channel |
| second-device code (4.4) | two device tokens held at once | needs a second device to enrol | cheap where two are paired | one-device hosted yard can never approve |

## 5. The recommendation: a fresh sign-in, and where it bites

This document recommends 4.1. A strong card is approved in one of
two places, and never with only a paired-device token:

- **on a hosted yard**, in the hosted web, where the request's
  gateway token was authenticated after the one-use challenge for
  this card, within the window (ten minutes, decision question 2),
  and maps to the operator; or
- **on any yard, hosted or not**, on the yard's socket — a terminal on
  the yard's machine, or a client on that socket — outside an agent's
  pane, as the firmest approvals are taken today.

A paired-device token, on the listener or down the edge link, reads a
strong card, puts it aside and declines it, and cannot approve it.
Its approval is refused, nothing runs, and the ask stays open, with
words that name the place: on a hosted yard, the web and its fresh
sign-in; elsewhere, a terminal on the yard's machine. The same
physical device may open the hosted web, but only with the separate
fresh account authentication. The weighed
word is still taken first, in its own refusal's words as today: the
stronger approval stands behind the weight gate, not in its place,
so a client that has never heard of weight is refused as it is today
and a client that has is then told the card needs more.

That this is a recommendation and not a decision is the yardmaster's
override on this bead, and it is kept in the words: section 8 puts the
choice to mckean with the other candidates whole. What follows is
written as the build would be if 4.1 is chosen, so that choosing it
costs no second design.

### 5.1 How a hosted approval runs

1. The card is written and weighed as today. Nothing about asking
   changes: the yardmaster proposes the command, the card shows the
   command whole, the yard's sentences, its weight lines and what
   takes it back.
2. The operator opens the card in the hosted web. The page shows the
   card as a terminal card does, weight first. Its approve control
   always starts a fresh gateway sign-in for a strong card, even when
   the current session is fresh. The web holds one-use state bound to
   this card and returns the operator to it after the gateway
   authenticates; no approval is attempted, and nothing is declined
   for them, while they sign in.
3. Within ten minutes after that card's challenge, the approve control
   approves once. The call is the
   approval the socket takes today (`ApproveAsk`, and `ApproveSetup`
   where a setup card is strong), made as the request's gateway user,
   saying `weighed` as a gated client does, and carrying no device
   token. In the one write the yard does what every approval does —
   the actor and pane checks, the link guard (`runCommand.link`: a
   peer never widens its own link on its own word), the stale and
   weight checks — and one check more, after `weighedRefusal` and
   before anything runs: the card is strong, and this approval's
   witness is the gateway's authentication time on this request,
   after the card's challenge, against the yard's clock at the write,
   within the window, for the operator the card is asked of. The web
   consumes the challenge state once and only for that card. A witness
   that is missing, stale, predates the challenge, was already used or
   maps to another user refuses with the strong refusal, records
   nothing and leaves the ask open. The witness is the door's statement on
   the call's context, as `via` and the reach mark are today
   (`internal/store/via.go`): the web sets it from the token it
   verified on this request, the socket's absence of it beside a
   person's pane is the terminal's, and no argument of any approval
   call carries it, so a device that adds a field gains nothing.
4. The answer and the change's own event record how the approval was
   witnessed, beside the `via` they carry today: `witness: fresh
   sign-in <time>` on a hosted approval, `witness: terminal` on the
   socket, and nothing of the kind claimed for a device, which never
   carries one. The card and event stream show the record to clients
   that read or follow them. Existing user subscriptions and a
   configured `notify` source may also tell the operator, subject to
   their `tell` settings and delivery success; this pick does not
   guarantee an alert to every paired device or outside address.

The window bounds the proof's lifetime, and one fresh sign-in
approves one strong card only. Each card is weighed and shown on its
own, as the batching rule already demands (a weighty card is approved
alone, by a call naming it, never in a batch). The short account
authentication happens after the card is shown; it does not promise
that the person read it, which section 5.5 says plainly. Decision
question 2 recommends a separate sign-in per card, at the cost of
another round trip on a day that links a peer and replaces a source
together.

Nothing is enrolled and nothing new is kept. The yard stores no
password, no passkey and no session of its own: the gateway's signed
authentication time is read on the request that approves, compared
in the write, and recorded as a time in the answer's event. A yard
whose gateway signs no authentication time — a self-hosted view
behind a gateway that omits it, a hosted provision that has not
turned it on — approves no strong card in the web, and says so in
the strong refusal rather than guessing from the token's age: a
token minted an hour ago for a session authenticated last week is
not a fresh sign-in, and treating its issue time as one would let a
standing session approve what this design exists to hold.

### 5.2 Which cards are strong

The test is the card's stored weight, read in the approval's write
exactly where `weighedRefusal` reads it today: **a card is strong
where its weight carries `trust` or `code`, and routine for this
design where it carries neither.** One class in the card makes the
whole card strong, as one class makes it weighty.

| class | strong? | the fact that settles it |
|---|---|---|
| `trust` | yes | it widens who the yard listens to — a link, a name trusted, reach, an endpoint, a user — and what it makes stands after the device is removed: it is the second way in itself |
| `code` | yes | it brings or replaces a program the yard will run, and the program stands after the device is removed: persistence by the yard's own hand |
| `credential` | no, alone | an API credential for an already-declared source does not admit a new yard user; the thief's own credential acts as them at that host. A user's address is also in this class, though: a `user key` card can redirect outside notifications and that change persists. This recommendation leaves that risk outside its trust/code rule. A credential on a card that also carries trust or code is witnessed by that class |
| `outside` | no, alone | it changes something at a service the yard already declared, through an existing yard actor, so it adds no yard user or code. Some outside changes can persist — an update grant or higher limit, for example — and that lasting external authority is outside this recommendation's narrow rule. An update grant on a card that also carries trust or code is witnessed by that class |
| `as-user` | no, alone | it lets calls already granted be made as a user whose credential the yard already holds; it admits no new party and leaves no program. A mapping pick and a user identity, which make an outside sender count as a user from then on, are the nearest miss, and decision question 4 names them for mckean rather than widening the test here |
| `reads` | no | it widens what an existing `read` grant can retrieve without adding a grantee or a program. That data-scope change persists; a plain `source grant read` is routine under today's weights, so neither case is protected by this recommendation |

These exclusions are scope limits, not claims that the effects are
harmless. A notification address can be redirected, outside grants can
persist, and a read declaration can expose more data to existing grantees;
each can outlive device removal. The recommendation protects the five
trust/code cards and does not promise that every lasting run-ask effect
needs a fresh sign-in. The security review should check whether that is
the right boundary.

That test is read from the weight as the reshaped five will weigh
it, and it binds the shaper of each: a form of one of the five that
can be approved must weigh trust or code in every case, or the form
stays in a terminal. Yardr-cpq7 is the one to watch — a replacement
whose command files are unchanged can still name a new host or a new
declared command in its text, which is code in everything but the
file inventory, and its reshaping weighs a differing declaration as
code rather than letting a host change ride through as routine.
Yardr-1bas likewise weighs a first install as code whether or not a
file happens to match a copy that stands. With that weighing, every
one of the five waiting cards carries trust or code, so every one is
strong.

The test also reaches cards that stand today, and says so: a `user
add` or `hook add` (trust), a `source add` or `pack apply` that brings
a new or changed command file (code), a setup whose pack brings
either. Those approvals move off the device by the same rule — on a
hosted yard to the web's fresh sign-in, on any yard to the terminal —
which is the point of a class test rather than a list of five
commands: a sixth trust card written next year is strong by its
weight or it is a hole. A card already open when the build lands is
read by the same test at its approval, so an old trust card gains the
refusal rather than keeping the tap it was written under.

And no routine card changes: a card with no weight, a
plain `read` grant, a mail limit lowered, a limit raised where it
lets no more updates through. Yardr-esmj's `peer rm` carries no
weight and takes standing away, so it stays a routine card approved
as today — the stronger approval must not stand in front of a
removal, and section 3 says why. Secret asks, which are weighty with
`credential`, keep the approval they have: the value itself is the
knowledge the giving proves, and the hosted giving down the link
(docs/users.md:903) is unchanged by this design. A setup ask is
strong exactly where its pack brings trust or code, which a first
install does; a setup that only takes an account is witnessed as
today, and the five's install (yardr-1bas) is not that case.

### 5.3 The one-device yard

A yard whose operator has exactly one paired device loses nothing
under the pick, and that is why it is the pick. The second possession
is the operator's hosted account, not a second device token: the
operator approves a strong card in a browser — on that same device,
if that is the device they have — by signing in fresh, and the yard
never counts devices. An already-fresh web session on that device
does not satisfy the card's new authentication challenge. An
operator with no paired device at all, a
hosted yard read only in the browser, approves a strong card the
same way, which no device-based candidate can say.

### 5.4 The self-hosted yard, and the terminal

A yard the operator runs themselves has the witness hosted yards
lack: the machine. A strong card there is approved on the socket —
`yardr bead answer … --approve` at a terminal on the yard's machine,
with the weight gate's typed number as today, or a client on that
socket outside an agent's pane — and by device nowhere, exactly as
on a hosted yard. No fresh sign-in is asked for, because there is no
hosted account to sign in to and the shell's possession is the
stronger fact the hosted web is standing in for. The operator who
reads the yard from a paired device away from the machine reads the
strong card, puts it aside, declines it, and approves it when they
are at the machine; a self-hosted yard behind a gateway of its own,
whose tokens carry an authentication time, may be given the hosted
road by the same build, and decision question 5 asks whether to
build that leg now or leave self-hosted to the terminal alone. What
this design will not do is approve a self-hosted strong card by
device on the strength of the device: that is the tap, on the yard
where the terminal makes it needless.

### 5.5 What the pick does not prove, said plainly

A fresh sign-in proves the operator's account was re-authenticated,
not that the operator read the card: the weight gate's showing and
the card's own yard-written sentences carry the reading, as they do
for every weighty card, and a person who signs in and taps through
is answered by those, not by this design. It proves nothing where
the account itself is compromised or its sign-in is phished inside
the window; section 3 puts that outside the threat. The remaining
handoff risk is the short interval after the gateway completes the
card's challenge and before the web consumes its one-use state. The
event is an audit record rather than a guaranteed alert. It trusts
the gateway and the edge that carries its token, as the yard already
trusts them for every hosted secret (docs/users.md:903). The yard
verifies the gateway's signed `auth_time`; the edge must carry that
token unchanged. A compromised edge can observe or withhold
hosted-request tokens as it can today, but cannot forge the gateway's
signed claim. And it leaves the window as a window:
ten minutes to complete one card-specific approval after
authentication, not proof that the operator read it. Each of those
is a reason the choice is mckean's, and
none of them is a reason the stolen device should approve a link.

## 6. The words for docs/users.md

When the build lands, the build edits docs/users.md; this design
carries the words so that edit is a paste, not a second design. The
block below replaces, in "Devices", the stolen-device table's lead
(docs/users.md:809-816), adds the five rows to that table in the
table's own order, replaces the cannot-have prose
(docs/users.md:850-864), and replaces the "Approvals cross the edge"
paragraph (docs/users.md:882-889) for strong cards. Rows for commands
already in the table are untouched except where a later section
says. The paste:

> **A stolen device of the operator can have the commands of a run
> ask run, and no other — and a strong card's command not by the
> paired-device token alone.** Whoever holds its token can approve every open run
> ask that is asked of the operator, except a strong one (below).
> They need not wait for one: a device cannot write a run ask, but it
> writes mail as the operator, and the yardmaster does what the
> operator asks by mail, so the holder can ask the yardmaster to
> propose a command and then approve it. So the commands a run ask may
> hold are what a stolen device of the operator can have run, with one
> agent's help, until `yardr user device rm` — with the one exception
> the next paragraph names. Row by row:
>
> [the table as it stands, with these five rows added:]
>
> | the command | what a holder of the token gains |
> |---|---|
> | `peer add <name> <invite>`: a peer linked from an invite | Nothing with only the paired-device token: the card is strong (trust). With the operator's fresh sign-in in the hosted web, or at a terminal on the yard's machine, the invited yard is linked and its calls and mail then cross as the invite says — a way in that stands after the device is removed |
> | `peer set <name> --url <url>`: where a peer is called moved | Nothing with only the paired-device token: the card is strong (trust). With a fresh sign-in or at a terminal, the link calls the moved address and the old address is stranded |
> | `peer allow <peer> --expose <names>`, `--trust <names>` or `--reach <name>@<peer>=<user>`: a link's trust widened | Nothing with only the paired-device token: the card is strong (trust). With a fresh sign-in or at a terminal, who is exposed, which names are trusted and whose asks are sent widen as the card showed; narrowing is anyone's and no card |
> | `source add <file>`, replacing a source that stands: a source's declaration replaced | Nothing with only the paired-device token: the card is strong (code) in every replacement form. With a fresh sign-in or at a terminal, the new declaration and each command file it names are installed as the card's diff showed them |
> | `pack apply <name>`, first install of an embedded depot pack to a named depot | Nothing with only the paired-device token where the pack brings a program or trust: the card is strong. A credential by itself does not make a card strong. With a fresh sign-in or at a terminal, the pack is installed to the named depot with the mappings and plan the card showed. A pack of kind source is installed with `bead ask --setup`, never this row |
>
> **Strong cards.** A card the yard weighs `trust` or `code` — it
> widens who the yard listens to, or it brings a program the yard
> will run — cannot be approved with only a paired-device token. On a hosted
> yard it is approved in the hosted web, in a session the gateway
> authenticated within ten minutes after the one-use challenge for
> that card, which the yard reads from the signed time the gateway
> gives and from no token of the yard's; on
> any yard it is approved on the yard's socket, at a terminal on the
> yard's machine. Everywhere the approval says how it was witnessed
> (`witness: fresh sign-in`, `witness: terminal`) beside the `via`
> it carries. The bead and event stream show the approval to clients
> that read or follow them; an outside notification depends on the
> operator's existing subscriptions and `notify` and `tell` settings,
> and successful delivery, so no alert to every paired device or
> outside address is promised. A card weighed
> neither trust nor code — a credential or an update taken alone, a
> routine card, and every narrowing, `peer rm` among them — is
> approved as it always was.
>
> What it still cannot have run, because no run ask may hold it:
> adding a device or pairing one, changing the operator, changing
> through which source a user is told or where their address is read
> from, replacing a hook or removing one, setting a hook's stage map
> or the crew member it acts as, reading a source's credential or
> setting where it is read from or the command that mints one,
> defining what a group runs or a crew member is, removing a source,
> and applying a pack yardr does not embed, with any flag, or where
> its plan asks. So a stolen device still cannot make itself a second
> way in — no new device, no new credential of its own, and the
> operator stands — and where a card would make one for it (a link
> added or widened, a program installed), the device alone is refused
> the card. Removing the device ends what it could do; what a
> properly witnessed approval changed stands until it is taken back,
> as before: the card and `yardr prime --bead` name the command that
> does.
>
> **Approvals cross the edge, and strong approvals do not ride a
> device token.** A paired-device token approves run and setup asks
> from wherever it reaches the yard, down the link from the edge
> included — except a strong card, which it cannot approve. The same
> physical device may open the hosted web after a fresh account sign-in.
> The edge sees the device's
> token on every call, so a dishonest or compromised edge, or a stolen
> phone that is unlocked, can approve exactly the command stored on
> an ask that is not strong. What holds it is that the command is
> fixed on the ask, that the operator paired the device, that the
> phone's unlock stands in front of the approval, which the yard
> cannot verify, and that `yardr user device rm` ends it at the next
> request. A strong card is held by one thing more: within minutes
> before it ran, after the card was opened, the operator's own account
> was signed in to again at the gateway, or the approval was made at a
> terminal on the yard's machine, and the device's token was no part
> of either.

Two notes on the paste. The ten minutes in "Strong cards" is the
window of section 5.1, named in user words as the build's constant;
if decision question 2 chooses another window, every occurrence of
that number in this document and the paste changes. The `pack apply`
row's first-install form covers depot packs only, as yardr-1bas scopes
it: a source-kind
pack's first install stays the setup ask's, so the same install never
has two routes.

## 7. Left out

- **Building any of it.** This document is the design and the words;
  section 9 lists the build and nothing here changes a call, a card
  or a doc that stands.
- **The five waiting beads themselves.** Yardr-r02z, yardr-tj0j,
  yardr-tegt, yardr-cpq7 and yardr-1bas are not edited here and are
  not reshaped onto this design here. They stay blocked behind this
  design and behind the build mckean chooses; reshaping them onto
  the chosen candidate is later shaper work, after it lands.
- **Changing routine-card approvals.** A card weighed neither trust
  nor code, a narrowing, and yardr-esmj's removal keep the approval
  they have, weighed word and all.
- **A passkey the yard holds, and a delay that runs a card.** Both
  are weighed whole in section 4 and rejected there for the fact
  each carries; neither is built as a fallback inside the pick. If
  mckean chooses one, its build is filed from section 4's account of
  it, not from section 9.
- **Removing the stolen device mid-design.** Device removal, token
  refusal at the next request and stream closing stand as they are
  (docs/users.md:960) and are the answer a noticed theft already has.
- **A second device as a quiet requirement.** No part of the pick
  counts paired devices. Where a second device is wanted, it is
  decision question 3, taken openly.
- **WebAuthn, a yard login, and a session of the yard's own.** The
  yard still authenticates nobody; the gateway's authentication time
  is read, never minted, by the yard.
- **Approving a strong card over a peer link, or in `yardr chat`
  by device.** A link carries no approval today and carries none
  here; chat shows the card and names the place, as it names the
  approving command today.

## 8. The decision asked of mckean

The pick is mckean's. Each question gives the recommended answer
first; choosing another answer chooses its candidate or its cost,
and the build beads of section 9 are filed for the choice made.

**1. Which stronger approval?** Option A, a fresh sign-in on the
hosted web, terminal on the yard's machine (section 5): no new
credential, works with one paired device, self-hosted keeps the
terminal it already trusts. Option B, a passkey the yard enrols
(section 4.2): the strongest proof per card, at the cost of yardr's
first authentication system. Option C, a delay with notice and
cancellation (section 4.3): no new possession at all, and a patient
thief outwaits it. Option D, a code from a second paired device
(section 4.4): two tokens, and a one-device hosted yard locked out.
This document recommends A.

**2. How fresh, and how often?** Option A, ten minutes, and one
sign-in may witness more than one strong card inside it: less
interruption, but an already-fresh session could be reused during the
window. Option B, ten minutes and one approval per card-specific
sign-in: fresh authentication after each card is shown, at another
round trip on a combined day. Option C, five minutes and one card
per sign-in: a shorter handoff window, and more often interrupted
mid-read. This document recommends B.

**3. Is a second paired device ever required beside the pick?**
Option A, no: the account is the second possession, and a one-device
operator approves by signing in for each card. Option B, yes, where
the operator has one: the fresh sign-in plus a second device's code,
for an operator who opts in. This document recommends A, and would
not make B the default for the reason 4.4 carries.

**4. Do identity cards join the strong test?** A mapping pick and a
`user identity` / `peer identity` change make an outside sender
count as a user from then on, which stands after a device is removed
as a link does, though their class today is `as-user` or none.
Option A, not now: the strong test stays trust and code, the five
land, and identity is weighed as its own question with the cards
that carry it. Option B, yes: the test is trust, code and any card
whose approval writes who a sender is. This document recommends A,
and names the miss plainly so the choice is taken knowingly.

**5. Does a self-hosted yard behind its own gateway get the web
road now?** Option A, not now: self-hosted approves strong cards at
the terminal, and the web road is built for the hosted gateway whose
authentication time is known. Option B, yes, wherever a gateway
token carries an authentication time that maps to the operator: one
rule for every yard, and a self-hosted operator away from the
machine gains the road. This document recommends A for the first
build, B as its named follow-on.

## 9. The build the pick needs — listed, not filed

One bead per mechanism piece, in the order they would land if
question 1 is answered with the recommendation. Each names which of
the five waiting beads rebases onto it; all five rebase onto the
whole set, since each is strong only when the gate, the witness and
the surfaces all stand. Reshaping the five onto the chosen build is
later shaper work, not these beads'. If another candidate is chosen,
this list is not filed: the yardmaster files that candidate's pieces
from section 4 instead.

**S1. The yard refuses a device's approval of a strong card.**
(depot yardr.) Goal: a card whose stored weight carries `trust` or
`code` is strong; `ApproveAsk` and `ApproveSetup` refuse an approval
that arrives through a device (`via` set) for a strong card, after
the weighed refusal and before anything runs, with words naming the
hosted web or the terminal; socket approvals carry `witness:
terminal`. Where to look: `approveAsk` in `internal/store/runask.go`
beside `weighedRefusal`, `approveSetup` in `runask_setup.go`,
`weight.go` for the predicate, `via.go` for the door's mark.
Done when: a device approval of a trust and of a code card is
refused and runs nothing, a socket approval of each runs, routine
and narrowing cards are unchanged by device, and the refusal is
tested per shape. The five rebase onto it: r02z, tj0j, tegt (trust);
cpq7, 1bas (code).

**S2. The web keeps the gateway's authentication time, and can say a
session is fresh.** (depot yardr.) Goal: `jwtVerifier` keeps each
verified token's authentication time beside its user and expiry,
refuses to call a token fresh where it carries none, and the web
can ask whether the acting operator's session is inside the window.
Done when: a token with a fresh, a stale and no authentication time
reads three ways in tests, and a fresh time can be compared with a
card-challenge time. The five rebase onto it: all, on hosted yards.

**S3. The hosted web approves a strong card under a fresh sign-in.**
(depot yardr.) Goal: the bead page approves run and setup cards
(weight first, the yard's sentences), starts a gateway sign-in for
every approval even if the session is fresh, binds one-use return
state to the card, and the store takes the approval only with the
request's fresh witness, recording `witness: fresh sign-in`. Docs:
docs/api.md for the witness, docs/web.md for the page, docs/users.md
with section 6's paste. Done when: a strong card is approved after
its challenge inside the window, refused outside it, without a time,
with a time predating the challenge, or for another user; a used
challenge cannot approve this or another card; and a device approval
stays refused after S1. The five rebase onto it: all, on hosted yards.

**S4. The edge asks its gateway to authenticate now.** (depot
yardr-hosted.) Goal: the hosted web's sign-in step for a strong card
re-authenticates at the gateway rather than accepting its standing
session, even when that session is fresh, and returns to the card.
Done when every strong-card challenge re-authenticates against the
edge's own gateway and returns to its card-specific state.
The five rebase onto it: all, on hosted yards.

**S5. Footplate and `yardr chat` name the place for a strong card.**
(depots footplate and yardr.) Goal: a device surface that cannot
approve a strong card says so in the strong refusal's words and
names the hosted web or the terminal, leaving the card open; no
device surface sends an approval S1 would refuse. Done when: a
strong card in each surface reads with its weight and its place,
and a routine card is unchanged. The five rebase onto it: all, for
the operator who reads by device.

**S6. Exercise strong approval end to end in an isolated yard.**
(depot yardr.) Goal: use `contrib/isolated-yard` to propose a strong
card and verify that the stand-in device is refused, the hosted web
approves after a fresh sign-in, and a terminal approves, with the
witness read back from each event. Its note says whether the sign-in
interrupted reading the card and whether the approval finished within
ten minutes after the challenge. Done when those paths pass in the
isolated yard and the real operator yard is untouched. Depends on
S1–S5. The five
rebase onto its finding: all.

Not listed: a bead for a passkey, a delay or a second device. Each
waits on its decision question, and filing one before it is answered
would file the choice this document leaves open.
