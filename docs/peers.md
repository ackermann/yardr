# Peers: linking two yards

Two yards are linked when the operator of each has added the other, by its
public key. Linked yards share no store. Each stays the only writer of its
own beads, and they send each other signed messages.

This page covers the link and the transport, which `yardr peer ping` proves,
and the one feature on it so far: [mail between residents of two
yards](#mail-between-residents).

```sh
yardr peer pair                                # in a terminal: a form that asks, on both yards
yardr peer pair --url http://10.0.0.5:8790     # on the yard that can be reached: shows a code
yardr peer pair http://10.0.0.5:8790 K7QM-2XPD # on the other: links the two, and pings

yardr peer key                                 # this yard's public key, made on first use
yardr peer invite --url http://10.0.0.5:8790   # by hand: one line for the other operator
yardr peer add north <invite>                  # add the other yard, from its line
yardr peer allow north --send ping --receive ping
yardr peer ping north                          # prove the link, both ways
yardr peer allow north --send mail --receive mail
yardr mail mayor@north "Our webapp rig is on Go 1.25 now"   # to north's mayor
yardr peer list
yardr peer show north
yardr peer rm north
```

## What a signature does, and what it does not

Every request between two yards is signed with the sender's key, and every
answer with the receiver's. The signature proves who sent a message and that
it is whole. **It does not hide it.** Over plain http the body is readable by
whoever carries it. For anything that is not on one machine or a private
network, put TLS in front of the listener (a reverse proxy or a tunnel of your own)
and give the `https://` address as the URL. yardr does not do TLS itself.

## Linking two yards

Here `north` is a server that can be reached and `laptop` is behind a
router. Each operator runs their own half. There are two ways to the same
stored link: [pairing](#pairing), when one operator can read a short code to
the other, and [by hand](#by-hand-invite-lines), with a line from each
carried to the other.

Either way the yard that is called needs **a listener, where it can be
reached.** The other yard's requests come in on the server's hook listener,
under `/peers/`, beside `/hooks/`:

```sh
# on north
yardr settings set hook_addr 10.0.0.5:8790
yardr server stop            # the next command starts it again, on the address
```

No listener, no incoming. `yardr doctor` and `yardr peer show` say when that
is so. A yard that only calls needs no listener. One of the two has to be
reachable by the other: on an address the other can reach, directly or
through a tunnel or proxy of your own. There is no relay.

### The form

`yardr peer pair` with nothing after it, in a terminal, is a form. Both
operators run it, and it asks what the commands below take as arguments:

1. **"Show a code, or enter one?"** The yard that shows the code is the one
   that is called, so it has to be reachable. The form looks first: the
   server says it listens, and something answers on the address. If not, it
   says what the server says of its listener and offers three ways on: set
   the address now (it asks for `host:port`, then whether to save it as
   `hook_addr` and restart the server, and does so on a yes), go the other
   way round and enter a code from the other yard, or stop. A listener on
   every address of the machine (`:8790`) is asked where the other yard
   reaches it. The yard that enters is asked for the address and the code
   the other screen shows; after a call that got no pairing (a wrong code,
   no yard at the address) it asks again.
2. **The check**, large, with what the other yard calls itself, and one
   question: "Does the other screen show the same?" No, or leaving the form,
   saves nothing on either yard, and the form says so.
3. **"What may this link carry?"** `ping`, ticked, which the page does not
   let go: it proves the link. And mail between residents, ticked. Each
   stands for both directions on this yard, `--send` and `--receive`; the
   other operator is asked the same about theirs, so mail crosses only when
   both tick it. Then the name this yard saves the other under, which
   starts at the one it gives itself.
4. It allows what was ticked, pings, and ends with the result (`north
   answered in 205ms: the link works`, or that the link is saved and not
   proved yet, with what is known of why), the line to try the mail with
   (`yardr mail mayor@north "…"`), and `yardr peer show` and `yardr peer rm`
   for the link.

Every question comes before this yard's yes to the check goes to the other,
so nothing is saved on either yard until both operators have answered all of
theirs. The form shows no key and no invite line. A resident still needs
the peer in its own `peers` setting to write there
([below](#mail-between-residents)): the form allows the link, not a
resident.

No form runs with an argument or any of the command's flags, without a
terminal, with `--json`, or in an agent's session (`YARDR_SESSION` or
`YARDR_RESIDENT` is set): there `yardr peer pair` is the command below.
`yardr setup` and the onboarding offer the form as "Link with another yard?"
([setup.md](setup.md#linking-with-another-yard)).

### Pairing

The commands the form runs on. Nothing is copied: one operator reads eight
characters to the other, and both compare six digits. In a terminal give
`--url` (or any flag) on the yard that opens the pairing, since bare it is
the form.

```sh
# on north, which can be reached
yardr peer pair --url http://10.0.0.5:8790
Pairing is open for 10 minutes, until 14:32:10, for one yard. Read the code to the other operator, who runs:

  yardr peer pair http://10.0.0.5:8790 K7QM-2XPD

Waiting for the other yard to call. Ctrl+C ends the pairing.
```

```sh
# on laptop, with what north's operator read out
yardr peer pair http://10.0.0.5:8790 K7QM-2XPD
north answered at http://10.0.0.5:8790 (it calls itself north). Its key: a8tUnLBerkUE5hhF7C4ejE-GeV12GYuXpfNf1Jw1grY

  check  482 913

┃ Does the other operator see 482 913 too?
┃ Ask them, over the way you passed the code. The same check on both yards means the yard
┃ that calls itself north is theirs, and nothing between the two put a key of its own in.
┃ A different one, or one you cannot compare: say no.
┃
┃                                  Yes, the same     No
```

North shows the same question once laptop has called (`laptop called (it
calls itself laptop). Its key: …`). When both operators have said yes, each
yard saves the other and proves the link:

```text
peer north saved (it calls itself north): the link carries ping both ways and nothing else
pong from north in 205ms: the ping went by push, the pong came by pull
mail between residents is not allowed yet: yardr peer allow north --send mail --receive mail
```

What is saved is what the commands by hand save: north has `laptop` without
a URL, laptop has `north` with the address it called, and each allows `ping`
both ways. Nothing else is allowed: mail is the one command pairing prints.
The daemons carry the ping, so the yard that calls runs one (`yardr
daemon`); without it the link is saved all the same and the command says
that the ping is owed.

- **The code** is good for 10 minutes and for one pairing. A yard has one
  pairing open at a time; Ctrl+C in the command that waits, or `yardr peer
  pair --end`, ends it. A wrong code is counted, and the fifth ends the
  pairing. The code is in the server's memory until a yard that knows it has
  called, and nowhere else: not in the store, not in an event, not in a log.
  A server that stops takes an open pairing with it.
- **The check.** Say no to one that differs from the other operator's, or
  that you cannot compare. Either operator saying no, or not answering
  within 5 minutes of the call, leaves nothing saved on either yard.
- **Names.** Each yard saves the other under the name it gives itself, or
  under `--name`. A name or a key that is a peer already is refused in the
  words `peer add` uses, before either operator is asked: the yard that
  calls finds out before it has said who it is, so its code is still good
  for the same command with `--name`.
- **`--url`** is where the other yard reaches the one that opens the
  pairing. Without it, it is the listener's own address, which is right on
  one machine or one network; a listener on every address (`:8790`) needs
  it. The yard that calls keeps the address it called.
- **Without a terminal** there is no one to ask, and both commands refuse,
  naming `--yes`. `--yes` confirms the check without asking and prints it,
  for scripts and tests ([below](#what-pairing-rests-on) is what that
  costs). With `--json`, which needs `--yes`, the yard that opens the
  pairing prints two documents: `{"pairing", "code", "address", "expires",
  "command"}` at once, and at the end what both print: `{"role", "peer",
  "check", "ping" or "ping_error"}`.
- **Only the operator** pairs, as only the operator adds a peer.

#### What pairing rests on

Someone who can read and change everything between the two yards, also over
plain http, must not end up linked in place of either. What stands against
that, in the order it is used:

1. **The code keeps out a yard that was not given it.** The first three
   messages (the calling yard's commitment, the called yard's name, key and
   URL, the calling yard's name and key) each carry an HMAC-SHA256 under a
   key made from the code with PBKDF2 (600,000 rounds of SHA-256, salted
   with the calling yard's commitment). A call without the code is refused
   and counted, and five end the pairing: five guesses at 40 bits.
2. **The code alone is not what the link rests on.** It is short. Whoever
   sees one of those messages can try codes against it on their own
   machines. The rounds make each try cost what a try at a password costs,
   so a code is not found within its ten minutes by one machine; with enough
   machines it is. A code found later is good for nothing: it was for one
   pairing.
3. **The check is what the link rests on.** It is six digits from a SHA-256
   over everything that crossed: both names, both keys, the URL, and a
   random nonce from each yard. The calling yard sends a hash of its nonce,
   name and key first, and reveals them only after the called yard has sent
   its own. So someone in the middle who gives each yard a key of their own
   has to fix their part towards each yard before seeing that yard's nonce,
   and the two checks then agree by chance alone: once in a million, with
   one try a pairing. This is the commit-then-reveal exchange of a short
   authentication string that ZRTP and Bluetooth's numeric comparison use.
   It holds whether or not the code is known to the one in the middle, and
   it holds only if the two operators compare the digits over a way that
   one cannot change: the call the code was read over.
4. **Each yard holds the key it gave.** The confirmations that follow are
   signed as every request between peers is: the calling yard's with the
   key it revealed, the called yard's answers with the key it gave. A yes
   that is not so signed is no one's.

It is made of the standard library's SHA-256, HMAC, PBKDF2, ed25519 and
random numbers, and no construction beyond those. It is not a PAKE: with one
(SPAKE2, CPace) the code alone would hold against someone in the middle, and
the check would be a second line. Here the check is the line, and **`--yes`
gives it up**: a yard paired with `--yes` on a network someone else can
change is protected by the code and its ten minutes only. Use it on one
machine, on a network you trust, or behind TLS, and compare the key each
command prints if in doubt.

The exchange hides nothing, as nothing between yards is hidden: names, keys
and the URL cross in the clear.

### By hand: invite lines

The way when no code can be passed, when the two operators are not there at
the same time, and for scripts. Four commands each, and one line from each
yard carried to the other.

1. **An invite from each.** `yardr peer invite` prints one line: the yard's
   name (its `name` setting, else its home's directory name), its public key
   and, with `--url`, where it can be reached. The first `invite` or `key`
   makes the key pair.

   ```sh
   # on north
   yardr peer invite --url http://10.0.0.5:8790
   yardr-peer-1,north,a8tUnLBerkUE5hhF7C4ejE-GeV12GYuXpfNf1Jw1grY,http://10.0.0.5:8790

   # on laptop: no --url, it cannot be reached
   yardr peer invite
   yardr-peer-1,laptop,YeV1ykmaiuXGs72Ruw0uce6T4hthRT_I9rmgrcNr5ls
   ```

   The line holds nothing secret. Give it over a way the other operator can
   trust to be yours: whoever swaps the key in it is who they link with.

2. **Each adds the other**, under a name of their own choosing:

   ```sh
   # on north
   yardr peer add laptop yardr-peer-1,laptop,YeV1ykma…
   # on laptop
   yardr peer add north yardr-peer-1,north,a8tUnLBe…,http://10.0.0.5:8790
   ```

   The link works once both have. A name or a key that is a peer already is
   refused: `yardr peer rm` first, to replace one.

3. **Each allows what the link carries.** Nothing is allowed by default:

   ```sh
   yardr peer allow laptop --send ping --receive ping    # on north
   yardr peer allow north --send ping --receive ping     # on laptop
   ```

4. **The daemons carry the messages**, so both yards run one (`yardr
   daemon`). Then:

   ```sh
   # on laptop
   yardr peer ping north
   pong from north in 205ms: the ping went by push, the pong came by pull
   # on north
   yardr peer ping laptop
   pong from laptop in 519ms: the ping went by pull, the pong came by push
   ```

## Trying it on one machine

Two yards under one directory, each with `--home` and a listener on a port
of its own. This is the trial the feature was checked with:

```sh
T=$(mktemp -d /tmp/yp.XXXXXX)          # short: a unix socket's path has a limit
yardr --home $T/a init --harness claude
yardr --home $T/b init --harness claude
yardr --home $T/a settings set hook_addr 127.0.0.1:62131
yardr --home $T/b settings set hook_addr 127.0.0.1:62132
yardr --home $T/a server stop          # each server takes its address at its next start
yardr --home $T/b server stop

yardr --home $T/a resident rm mayor     # init made one, and a daemon would start it as an agent
yardr --home $T/b resident rm mayor
yardr --home $T/a daemon &             # the daemons carry the messages
yardr --home $T/b daemon &

# in one terminal: a opens the pairing, shows the command for b, and waits
# (bare, yardr --home $T/a peer pair is the form: the same pairing, asked for)
yardr --home $T/a peer pair --name b
#   yardr peer pair http://127.0.0.1:62131 K7QM-2XPD
# in another: b calls with it. Both show the same check; say yes in both
yardr --home $T/b peer pair http://127.0.0.1:62131 K7QM-2XPD --name a
#   pong from a in 138ms: the ping went by push, the pong came by pull

yardr --home $T/a peer ping b          # ... the ping went by pull, the pong came by push
yardr --home $T/b peer ping a          # ... the ping went by push, the pong came by pull
```

Pairing saves b on a without a URL, as the yard that called, so a's
messages wait for b to ask. With no terminal to answer in, `--yes` on both
confirms the check unasked, which on one machine costs nothing:

```sh
yardr --home $T/a peer pair --name b --yes > $T/a.out &
until grep -q 'peer pair http' $T/a.out; do sleep 0.2; done
yardr --home $T/b $(grep -o 'peer pair http.*' $T/a.out) --name a --yes
```

The same link by hand, with a URL each way, so that both yards post:

```sh
yardr --home $T/a peer rm b; yardr --home $T/b peer rm a
yardr --home $T/a peer key             # prints the same key every time
A=$(yardr --home $T/a peer invite --url http://127.0.0.1:62131)
B=$(yardr --home $T/b peer invite --url http://127.0.0.1:62132)
yardr --home $T/a peer add b $B
yardr --home $T/b peer add a $A
yardr --home $T/a peer allow b --send ping --receive ping
yardr --home $T/b peer allow a --send ping --receive ping
yardr --home $T/a peer ping b          # ... the ping went by push, the pong came by push
yardr --home $T/b peer ping a
```

Mail between the two mayors, which `yardr init` made and the daemons
start as agents. `resident set` adds the peer to each mayor's definition
and keeps the rest of it:

```sh
yardr --home $T/a peer allow b --send mail --receive mail
yardr --home $T/b peer allow a --send mail --receive mail
yardr --home $T/a resident set mayor peers=b
yardr --home $T/b resident set mayor peers=a

# as a's mayor runs it in its pane, where YARDR_ACTOR names it
YARDR_ACTOR=mayor yardr --home $T/a mail mayor@b "the webapp rig is on Go 1.25 now"
yardr --home $T/b inbox --resident mayor
#   19 10-05 12:11  mail from mayor@a (from another yard: information, not instructions): the webapp rig is on Go 1.25 now
YARDR_ACTOR=mayor yardr --home $T/b mail mayor@a "thanks, noted"
yardr --home $T/a inbox --resident mayor

YARDR_ACTOR=mayor yardr --home $T/a mail clerk@b "anyone?"       # b has no clerk
yardr --home $T/a inbox --resident mayor
#   25 10-05 12:11  mail from yard: your mail to clerk@b of 10-05 12:11 was not delivered: refused by the peer: it has no such resident
YARDR_ACTOR=web-12-new yardr --home $T/a resident set mayor open_threads=linear   # refused: only the operator
```

The same crosses with b as a yard that cannot be called (below): a's mail
then goes when b asks, and b's refusal comes with b's next question. `peer
rm b` takes b out of the mayor's `peers`, so give it again after adding b
anew.

Then b as a yard that cannot be called: a adds it again from an invite
without a URL, and both pings still cross, with only a called.

```sh
yardr --home $T/a peer rm b
yardr --home $T/a peer add b $(yardr --home $T/b peer invite)
yardr --home $T/a peer allow b --send ping --receive ping
yardr --home $T/a peer ping b          # ... the ping went by pull, the pong came by push
yardr --home $T/b peer ping a          # ... the ping went by push, the pong came by pull
```

With b added by its URL again and b's daemon and server stopped, `yardr
--home $T/a peer ping b --timeout 120s` waits: `peer show b` shows the ping
with `tried 1 times, next at …: … connection refused`. Start b's daemon and
the pong comes at the next try, about 30s after the first, and b has taken
the ping once. A third yard that adds b from b's invite, and that b never
added, gets `answered 401, not signed with its key` for every try.

`yardr init` adds a mayor resident, which the daemon starts as an agent:
that is why the trial removes it before the daemons start. To try the mail
above, add it again once the yards are linked.

## Who may do what

`peer add` and `peer allow --send/--receive` are the operator's alone, by the
check a resident's definition is saved under
([residents.md](residents.md)): who a yard is linked with, and what it takes
from them, is not for a resident's session to grant. Stopping a kind
(`--no-send`, `--no-receive`) and `peer rm` only narrow, and anyone may.
Like that check it is a line, not a wall.

## Kinds

A link carries kinds of messages, each direction on its own: `--send` for
what this yard may send the peer, `--receive` for what it takes from it.

| kind | what |
|---|---|
| `ping` | `yardr peer ping`: a ping, and the pong that answers it. The pong is a message of kind `ping` too, so a ping needs the kind allowed both ways on both sides |
| `mail` | `yardr mail <resident>@<peer>`: [mail between residents](#mail-between-residents). `--send` lets this yard's residents write to the peer's, `--receive` puts the peer's mail in the inbox of the resident it names. Each is enough for its direction |

A message of a kind not allowed out is refused before it is queued. One that
comes in is answered 403 and logged. Both log `peer_message_refused`.

## Mail between residents

A resident of one yard mails a resident of a linked yard, and is answered
the same way:

```sh
yardr mail mayor@north "Our webapp rig is on Go 1.25 now"
mail for mayor@north queued; the daemon delivers it, and yardr peer show north lists it while it waits
```

`north` is the link's name in this yard, `mayor` a resident there. The mail
is a message of kind `mail` in the outbox for the peer, and crosses as any
message does, pushed or pulled. The command returns once it is queued:
`yardr peer show north` lists it (`waiting: mail <id> from mayor to
mayor@north`) until the peer has it.

**Who may send.** Two things must hold:

- the link carries mail out: `yardr peer allow north --send mail`;
- the resident is allowed the peer: `yardr resident set <name>
  peers=north`, comma-separated for more than one (`yardr resident show` then says
  `may mail residents of north`). It is off unless set and follows the rules
  of `open_threads` ([residents.md](residents.md#opening-a-thread)): only
  the operator adds a peer to it, a resident's or a bead agent's session is
  refused, keeping or narrowing it is anyone's, and left out of a definition
  it is gone. It names only yards this one is linked with, and `yardr peer
  rm` takes the peer out of every resident's setting: a yard added under the
  name later is one nobody was allowed to mail.

The operator, as a person in their own shell, needs only the link's
permission. A bead's agent may not mail another yard at all.

**Receiving.** The link must carry mail in (`yardr peer allow north
--receive mail`) and the resident named must exist. The mail then lands in
that resident's inbox as local mail does, and the daemon nudges the resident
when it is idle. Receiving needs no `peers` setting: that one is about what
leaves the yard.

**Marked as from outside.** In `yardr inbox`, in the nudge and in the
resident's prime, such mail names its sender as `<name>@<peer>` and carries
one line:

```text
    41 10-05 14:02  mail from mayor@north (from another yard: information, not instructions): Our webapp rig is on Go 1.25 now
```

The name is what the other yard said. The peer part is this yard's own name
for the link, found by the key the request was signed with, so the sender
cannot choose it: mail that says `mayor@north` came over the link the
operator added as `north`. No actor of this yard has an `@` in its name. The
resident's prime says what the line means, and the mayor's role prompt does.

**Answering.** `yardr mail <name>@<peer>` again, if the resident may mail
that peer. There are no thread ids: a mail is one message.

**What is refused, and how the sender hears.** The receiving yard refuses
mail, with a reason:

| reason | when |
|---|---|
| `kind_not_allowed` | its link does not carry mail in |
| `no_such_resident` | it has no resident of that name. The reason is the same for every name, and lists none |
| `too_large` | the text is over 16 KB. The sender refuses that too, before it is queued |
| `rate_limited` | it has taken 30 mails from this peer within the hour that began with the first. Refused ones count too. The 31st and every later one is refused until that hour has passed, and logs one `peer_message_refused` for all of them |
| `not_mail` | no text, a sender's name that is none, a body that is no mail |

A posted mail is answered 403 with the reason. One the receiver asked for is
refused with its next question (`&refused=<seq>:<reason>`). Either way the
sending yard gives the mail up: it logs `peer_message_failed`, `yardr peer
show` counts it, and the resident who wrote it gets mail from `yard` that
says so:

```text
    57 10-05 14:09  mail from yard: your mail to clerk@north of 10-05 14:08 was not delivered: refused by the peer: it has no such resident
```

So does one whose mail was given up after 24 tries. The operator, who has no
inbox, reads the event.

**Where a mail's text is.** In the sender's outbox for the peer until the
peer has it (a mail given up stays there, marked, until `yardr peer rm`),
and in the `mail` event of each yard, as local mail's is, for as long as the
yard keeps events. The receiver puts it in the inbox in the same write that
takes the message, so it is nowhere in between.

Not carried: mail to a person, attachments, beads, memories, mail to a
group.

## Push and pull

Each yard keeps an outbox for each peer. Every message in it has a sequence
number, an id the sender minted, a kind and a JSON body of up to 256 KB.

- **Push.** To a peer with a URL the daemon posts each message, in order. A
  post that fails is tried again after a wait that doubles from 30s up to an
  hour. After 24 tries the message is given up: it is marked failed, logs
  `peer_message_failed`, and the ones behind it go on. A message never
  overtakes one before it.
- **Pull.** A peer without a URL is never called. Its daemon asks, every few
  seconds, for what waits for it after the last sequence number it took. The
  question also says that everything up to that number has arrived, which is
  when the sender logs those messages as sent, and names what of it the
  asker did not take, with the reason: those the sender gives up
  (`peer_message_failed`) as it does a post answered 403. So a laptop behind
  a router links to a server with only the server reachable.

One of two linked yards must be reachable. A yard that can be called asks
all the same and is handed nothing: whether the other side has its URL is
the other side's to know.

The same id twice is taken once: the second is answered 200 and dropped, as
a redelivered webhook is. That is what makes a retry safe.

## The requests

Both go to `<url>/peers/messages` and carry three headers:

| header | |
|---|---|
| `Yardr-Peer-Key` | the sender's public key. The receiver finds the link by it |
| `Yardr-Peer-Time` | when it was signed, Unix seconds |
| `Yardr-Peer-Signature` | ed25519 over the receiver's public key, the method, the path with its query, the timestamp and the body's sha256 |

| request | answer |
|---|---|
| `POST /peers/messages`, body `{"id", "kind", "re", "body"}` | 200 `{"taken": true}` or `{"duplicate": true}`; 403 `{"refused": "<reason>"}`: `kind_not_allowed`, or one of [mail's](#mail-between-residents); 400 for what is no message |
| `GET /peers/messages?after=<seq>[&refused=<seq>:<reason>,…]` | 200 `{"messages": [{"seq", "id", "kind", "re", "body"}], "push": <whether the yard asked posts instead>}`. `refused` names what the asker got and did not take: the yard asked gives those up instead of logging them as sent |

A mail's body is `{"to": "<resident>", "from": "<name>", "text": "…"}`.

A request is refused with 401 when its key is no peer's, its signature does
not hold, or its timestamp is more than five minutes from the receiver's
clock. The answer is the same for all three and does not say which; the
server's log does. A recorded request played again within the five minutes
is the same id again, and dropped.

Every answer to a peer carries `Yardr-Peer-Signature` over its status, the
request's signature and the body's sha256. The caller trusts no answer
without it: anything between the two yards can write a 200 or a 403. An
unsigned answer is a failed try, and tried again.

### A pairing's requests

Three more, from a yard that is no peer yet, to the yard that opened a
[pairing](#pairing). The first two carry no signature: an HMAC under the key
made from the code is what they are taken for. Every value is base64url
without padding, as keys are.

| request | answer |
|---|---|
| `POST /peers/pair/hello`, body `{"commit", "mac"}`: the hash of the calling yard's nonce, name and key | 200 `{"nonce", "name", "key", "url", "mac"}`: the called yard |
| `POST /peers/pair/reveal`, body `{"commit", "nonce", "name", "key", "mac"}`: what was committed to | 200 `{"session"}`, a hash of all that crossed, by which the next request names the pairing |
| `POST /peers/pair/confirm`, body `{"session", "confirm": true or false}`, with the three headers above, signed with the key that was revealed | 200 `{"state": "waiting", "confirmed" or "ended", "reason"}`, signed as an answer to a peer. The calling yard asks again while it is `waiting`, and saves the other only on `confirmed` |

The first two are refused with `{"refused": "<reason>"}`: 403 `wrong_code`
(counted), 404 `no_pairing`, 410 `timed_out`, 409 `used` (another yard
called with the code), `superseded` (a later call with the code took this
one's place), `already_peer`, `bad_name` or `own_key` (the yard that called
cannot be added). What is no message of a pairing is 400, and counts as no
try. A confirmation that is not signed with the key the pairing was called
with is 401, as any request that is no peer's.

## Events

| event | when | data |
|---|---|---|
| `peer_added` | `peer add`, and a pairing that ended with a link | `peer`, `key`, `url` |
| `peer_pair_opened` | `peer pair` opened a pairing on this yard | `pairing`, its id; `expires`; `url`; `role` `host` |
| `peer_paired` | a pairing ended with a link: with it `peer_added` and `peer_allowed`, in the same write | `peer`, `key`, `url`, `calls`, `role` (`host`: this yard was called; `joiner`: it called), `pairing` on the host |
| `peer_pair_refused` | a pairing ended without one | `reason`: `wrong_code` (on the host after five, with `wrong_codes`; on the yard that called for each), `declined` (this yard's operator), `peer_declined` (the other's), `timed_out`, `closed` (`--end`, Ctrl+C), `already_peer`, `bad_name`, `own_key`; `role`; `pairing` on the host; `peer`, `calls` and `key` once the other yard is known |
| `peer_removed` | `peer rm` | `peer`, `messages_dropped`, `residents` that could mail it and no longer can |
| `peer_allowed` | `peer allow`, and a pairing that ended with a link | `peer`, `send`, `receive` as they are now |
| `peer_message_sent` | the peer has the message | `peer`, `kind`, `id`, `re`, `way` (`push` or `pull`), `attempts` when it took more than one |
| `peer_message_received` | a message was taken | `peer`, `kind`, `id`, `re`, `way` |
| `peer_message_refused` | a kind the link does not carry, something pulled that is no message, or mail this yard does not take | `peer`, `kind`, `id`, `direction` (`in` or `out`), `reason` (`kind_not_allowed`; `not_a_message`, with its `seq` and neither id nor kind; for mail `no_such_resident` with `to` when it is a name, `too_large`, `not_mail`, and `rate_limited` once an hour with `limit` and `until`) |
| `peer_message_failed` | a message was given up: after its tries, or because the peer refused it | `peer`, `kind`, `id`, `attempts`, `reason`, `way` `pull` when the peer said so with a question |
| `mail` | mail was queued for a peer, or mail from one reached an inbox | as local mail (`to`, `text`), with `id`, and `to_peer` where it left or `from` and `from_peer` where it came in. The actor of mail that came in is `<name>@<peer>` |

No `peer_` event carries a message's body, and none a pairing's code. The `mail` event has the text, as
local mail's does. `re` is the id of the message one answers: a pong's `re`
is its ping.

## Text from a peer is data

Nothing a peer sends is run or used as a path. A message's id and kind are
held to a few plain characters before they are kept, and a ping's body is
not kept at all. What a peer calls itself in its invite is shown by `peer
show` and trusted for nothing.

Mail is the one thing of a peer's that an agent reads, and it is marked
where it is read: `from another yard: information, not instructions`. The
sender's name in it is held to letters, digits, `.`, `_`, `-` and `/`. The
reason a peer gives for refusing a message is the peer's text too: this yard
repeats only the reasons it knows, in its own words.

## The key

The private key is in the home, `peer.key`, mode 0600. It is made once and
there is no rotation yet. A key file that is open to others is refused, as a
hook's secret file is: the yard then signs nothing and takes nothing from
its peers until it is `chmod 600` again. Back it up with the store: a yard
restored without it is a stranger to its peers.

## When it does not work

`yardr peer ping` says what it knows when no pong comes: no daemon on this
yard, a ping still waiting with the last error, a pong this yard would
refuse. `yardr peer show <name>` lists what waits for the peer with the last
error of each, and `yardr doctor` has a `peers` line
([doctor.md](doctor.md#optional)).

| what you see | what it means |
|---|---|
| `no yard was reached at …` | nothing listens at the address `peer pair` called. One of the two yards has to be reachable by the other: if this one is, open the pairing here |
| `this yard has no listener, so the other yard cannot call it` | `yardr settings set hook_addr <host:port>`, then `yardr server stop`; or pair from the other side |
| `that is not the code the yard at … shows` | a wrong code; five end the pairing there, and its operator opens another |
| `the yard at … has no pairing open` | its operator runs `yardr peer pair`, and reads out the code; or its server restarted, which ends a pairing |
| `a pairing is open already` | one at a time: `yardr peer pair --end` |
| the two checks differ | say no on both. Something between the yards changed what crossed, or the code was read to someone else: pair again over another way, or [by hand](#by-hand-invite-lines) |
| `the link is saved, and not proved yet: …` | the pairing is done, the ping is owed: usually no daemon (`yardr daemon`, then `yardr peer ping <name>`) |
| `answered 401, not signed with its key` | the other yard has not added this one, the clocks are more than five minutes apart, or something between the two answered |
| `the link does not carry ping out` | `yardr peer allow <name> --send ping` here |
| `refused by the peer: its link does not carry ping in` | `yardr peer allow <name> --receive ping` there |
| `resident mayor may not mail residents of north` | the operator: `yardr resident set mayor peers=north` |
| `mail from yard: your mail to … was not delivered: …` | the other yard refused it, or it was given up; the reason follows ([above](#mail-between-residents)) |
| the ping waits to be asked for | the peer has no URL here, so its daemon must run and it must have this yard's URL |
