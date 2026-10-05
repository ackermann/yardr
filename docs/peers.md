# Peers: linking two yards

Two yards are linked when the operator of each has added the other, by its
public key. Linked yards share no store. Each stays the only writer of its
own beads, and they send each other signed messages.

This page covers the link and the transport, which `yardr peer ping` proves.
The link carries no feature yet: mail between residents of two yards is
*planned, yardr-xbyy*, on the same transport.

```sh
yardr peer key                                 # this yard's public key, made on first use
yardr peer invite --url http://10.0.0.5:8790   # one line for the other operator
yardr peer add north <invite>                  # add the other yard, from its line
yardr peer allow north --send ping --receive ping
yardr peer ping north                          # prove the link, both ways
yardr peer list
yardr peer show north
yardr peer rm north
```

## What a signature does, and what it does not

Every request between two yards is signed with the sender's key, and every
answer with the receiver's. The signature proves who sent a message and that
it is whole. **It does not hide it.** Over plain http the body is readable by
whoever carries it. For anything that is not on one machine or a private
network, put TLS in front of the listener (a reverse proxy, Tailscale Funnel)
and give the `https://` address as the URL. yardr does not do TLS itself.

## Linking two yards

Here `north` is a server that can be reached and `laptop` is behind a
router. Each operator runs their own half.

1. **A listener, where the yard can be reached.** The other yard's requests
   come in on the server's hook listener, under `/peers/`, beside `/hooks/`:

   ```sh
   # on north
   yardr settings set hook_addr 10.0.0.5:8790
   yardr server stop            # the next command starts it again, on the address
   ```

   No listener, no incoming. `yardr doctor` and `yardr peer show` say when
   that is so. A yard that only calls needs no listener.

2. **An invite from each.** `yardr peer invite` prints one line: the yard's
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

3. **Each adds the other**, under a name of their own choosing:

   ```sh
   # on north
   yardr peer add laptop yardr-peer-1,laptop,YeV1ykma…
   # on laptop
   yardr peer add north yardr-peer-1,north,a8tUnLBe…,http://10.0.0.5:8790
   ```

   The link works once both have. A name or a key that is a peer already is
   refused: `yardr peer rm` first, to replace one.

4. **Each allows what the link carries.** Nothing is allowed by default:

   ```sh
   yardr peer allow laptop --send ping --receive ping    # on north
   yardr peer allow north --send ping --receive ping     # on laptop
   ```

5. **The daemons carry the messages**, so both yards run one (`yardr
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

yardr --home $T/a peer key             # prints the same key every time
yardr --home $T/b peer key
A=$(yardr --home $T/a peer invite --url http://127.0.0.1:62131)
B=$(yardr --home $T/b peer invite --url http://127.0.0.1:62132)
yardr --home $T/a peer add b $B
yardr --home $T/b peer add a $A
yardr --home $T/a peer allow b --send ping --receive ping
yardr --home $T/b peer allow a --send ping --receive ping

yardr --home $T/a daemon &             # the daemons carry the messages
yardr --home $T/b daemon &

yardr --home $T/a peer ping b          # ... the ping went by push, the pong came by push
yardr --home $T/b peer ping a
```

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
`yardr --home $T/a resident rm mayor` before the daemon, in a yard made only
to try this.

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

A message of a kind not allowed out is refused before it is queued. One that
comes in is answered 403 and logged. Both log `peer_message_refused`.

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
  when the sender logs those messages as sent. So a laptop behind a router
  links to a server with only the server reachable.

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
| `POST /peers/messages`, body `{"id", "kind", "re", "body"}` | 200 `{"taken": true}` or `{"duplicate": true}`; 403 `{"refused": "kind_not_allowed"}`; 400 for what is no message |
| `GET /peers/messages?after=<seq>` | 200 `{"messages": [{"seq", "id", "kind", "re", "body"}], "push": <whether the yard asked posts instead>}` |

A request is refused with 401 when its key is no peer's, its signature does
not hold, or its timestamp is more than five minutes from the receiver's
clock. The answer is the same for all three and does not say which; the
server's log does. A recorded request played again within the five minutes
is the same id again, and dropped.

Every answer to a peer carries `Yardr-Peer-Signature` over its status, the
request's signature and the body's sha256. The caller trusts no answer
without it: anything between the two yards can write a 200 or a 403. An
unsigned answer is a failed try, and tried again.

## Events

| event | when | data |
|---|---|---|
| `peer_added` | `peer add` | `peer`, `key`, `url` |
| `peer_removed` | `peer rm` | `peer`, `messages_dropped` |
| `peer_allowed` | `peer allow` | `peer`, `send`, `receive` as they are now |
| `peer_message_sent` | the peer has the message | `peer`, `kind`, `id`, `re`, `way` (`push` or `pull`), `attempts` when it took more than one |
| `peer_message_received` | a message was taken | `peer`, `kind`, `id`, `re`, `way` |
| `peer_message_refused` | a kind the link does not carry, or something pulled that is no message | `peer`, `kind`, `id`, `direction` (`in` or `out`), `reason` (`kind_not_allowed`; `not_a_message`, with its `seq` and neither id nor kind) |
| `peer_message_failed` | a message was given up | `peer`, `kind`, `id`, `attempts`, `reason` |

No event carries a message's body. `re` is the id of the message one
answers: a pong's `re` is its ping.

## Text from a peer is data

Nothing a peer sends is run, used as a path, or shown to an agent. A
message's id and kind are held to a few plain characters before they are
kept, and its body is not kept at all: no kind there is yet has a use for
it. What a peer calls itself in its invite is shown by `peer show` and
trusted for nothing.

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
| `answered 401, not signed with its key` | the other yard has not added this one, the clocks are more than five minutes apart, or something between the two answered |
| `the link does not carry ping out` | `yardr peer allow <name> --send ping` here |
| `refused by the peer: its link does not carry ping in` | `yardr peer allow <name> --receive ping` there |
| the ping waits to be asked for | the peer has no URL here, so its daemon must run and it must have this yard's URL |
