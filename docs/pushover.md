# A push to your phone: Pushover

This page sets up one thing: what lands in a user's inbox (mail, a timer,
a notification of a subscription) buzzes that person's phone through
[Pushover](https://pushover.net). yardr ships it as a pack: `yardr pack
apply pushover` and two commands that each take one key set it up whole.
No key is written to a file by hand.

yardr has no Go for Pushover. The whole integration is the declaration the
pack brings, with its `notify` key; [below](#by-hand) it is written out,
as the example for another service that takes one HTTPS request with a
JSON body.

## What you need

- yardr with its server and daemon running (`yardr status`), and a user in
  it for the person who is told. The operator is one already: `yardr user
  list`. Someone else is added by the operator with `yardr user add <name>`.
- A Pushover account, and its app on the phone, logged in.

The steps use `mckean` as the user. Put your own name there.

## 1. Two keys from Pushover

On pushover.net, logged in:

- **Your User Key**: on the first page after login, under "Your User Key".
  30 letters and digits. It is where *you* are reached.
- **An application token**: "Create an Application/API Token"
  (pushover.net/apps/build), with any name, say `yardr`. It gives an "API
  Token/Key", also 30 letters and digits. It is the *yard's*.

Pushover's free allowance is 10,000 messages a month for an account; yardr
sends one user at most 20 an hour.

## 2. Apply the pack

As the operator, with the user told through it:

```sh
yardr pack apply pushover --notify mckean=pushover
```

It answers with the source and the user, who is not reached yet:

```
source pushover is declared: 1 call under https://api.pushover.net/1 (yardr source show pushover)
user mckean: told outside the yard: through pushover, which is not set up: user mckean has no env_file: yardr user key mckean pushover gives PUSHOVER_USER
```

then the pack's setup notes (`yardr pack show pushover` prints them
later). `yardr user list` shows who is told through what.

## 3. Give the yard the two keys

Each key is typed or pasted into a command that shows nothing of it, and
the yard's server writes it into a file only you can read. Nobody writes
those files by hand, and the keys go nowhere else: not into mail, chat, a
bead or a pack.

```sh
yardr source key pushover           # PUSHOVER_TOKEN, the application token
yardr user key mckean pushover      # PUSHOVER_USER, your user key
```

```
PUSHOVER_TOKEN for the source pushover (nothing is shown as you type):
source pushover: PUSHOVER_TOKEN is set in /Users/mckean/.yardr/sources/pushover.env
PUSHOVER_USER for the source pushover (nothing is shown as you type):
user mckean: PUSHOVER_USER is set in /Users/mckean/.yardr/users/mckean.env
next: yardr source check pushover --user mckean sends one test message
```

The application token is the yard's and the operator's to give
([`yardr source key`](integrations.md)). The user key is the person's own:
the user gives it, or the operator for them, and it is refused from the
pane of an agent ([users.md](users.md#told-outside-the-yard)). yardr keeps
the two paths and reads the files each time it sends.

**Or on a card.** A crew member that sets this up asks for each key with a
secret ask, and you answer where the field shows dots: in footplate, or in
`yardr chat` with `ctrl+o` ([chat.md](chat.md#questions-for-you)). The
value goes from that field to the yard's server and into the same file,
and no agent is shown it.

```sh
yardr bead ask <bead> "Pushover's application token" --secret pushover \
  --context "Create one at pushover.net/apps/build, with any name."
yardr bead ask <bead> "Your Pushover user key" --secret pushover --for-user mckean \
  --context "It is on the first page of pushover.net after login: Your User Key."
```

The first asks the operator; the second asks `mckean`, and is theirs or
the operator's to answer.

## 4. One test message

```sh
yardr source check pushover --user mckean
```

The phone buzzes with "yardr: a test", and the command prints `pushover:
told mckean: 200 in …ms`. This is one real message through Pushover, sent
by the command itself. Anything but 200 fails the command with the status:
400 from Pushover is a token or a user key it does not take, so give the
two keys again (step 3): a key given again replaces the one before.

## 5. Something to be told of

Mail to the user and their own timers are told from now on. A bead that
needs a person is a subscription:

```sh
yardr subscribe mckean yardr --where stage=decide      # every bead that enters decide
```

Then, to see it end to end without waiting for a bead:

```sh
yardr mail mckean "the tests are green, come and look"
```

About 20 seconds later the phone shows `mail from <you>` and the text.

## What is sent, and when

- **What**: mail to the user (their own too, and a timer set for them), the
  notifications of their subscriptions, and every ask for them. Nothing
  else, and nothing for a crew member. Fewer with `yardr user set mckean
  tell=notifications` (a list of `notifications`, `mail`, `asks`): only
  what was subscribed to, and no push for each mail
  ([users.md](users.md#told-outside-the-yard)).
- **What leaves the machine**: a fixed line that says who and what kind
  (`mail from yardmaster`, `timer from mckean`, `web-hk7z advanced`), and
  the first words: of mail the first 200 characters, of a notification its
  line as `yardr inbox` shows it, with the bead's title, of an ask
  (`yardmaster asks`) the first 200 characters of the question. Several at
  once are `3 new in your yardr inbox`, or `3 new in yardr` with an ask
  among them, and each kind with its count. No link is
  sent. This goes to Pushover's servers and onto a lock screen: do not name
  a source for a user whose mail must not.
- **When**: once it has been in the inbox for 20 seconds and is still
  unread. What was read by then (`yardr chat` showing it, `yardr inbox
  --ack`) is not sent, nor is an ask that was answered by then. Everything due for one user goes as one message, and
  a burst waits until nothing new came for 20 seconds, 60 at most.
- **How many**: at most 20 messages an hour for one user. The twentieth
  begins "The 20th message this hour, and the last: what comes next waits
  in yardr inbox."; the rest goes as one message when the hour has room.
- **When Pushover does not answer**: the inbox has everything all the same;
  it never waits for a source. The daemon tries again after 30 seconds,
  then with doubling waits, for up to 8 tries and 6 hours, then gives up
  with a `push_failed` event. A 4xx other than 408 and 429 is final at
  once. `yardr user show mckean` has how the last one ended (`last told:
  …`), and `yardr doctor` notes a user who was not told.

Of these only the first is a setting (`tell`); the rest is not yet.

## Ending it

```sh
yardr user set mckean notify=        # nobody is told; what was still waiting is dropped
yardr source rm pushover             # the source; the two files stay, and are yours to delete
```

## By hand

Without the pack, the same setup from nothing — and the worked example of
a source that [tells a user outside the yard](integrations.md#tell-a-user-outside-the-yard),
for another service that takes one HTTPS request with a JSON body, with
another file. Save this as `pushover.yaml`, anywhere. It holds no secret:
it names the variables of step 2, never their values.

```yaml
name: pushover
about: A push to a person's phone through Pushover.
base: https://api.pushover.net/1
credential:
  env: PUSHOVER_TOKEN                 # the application's token: the name, never the value
calls:
  message:
    kind: update
    about: One push to one person
    params:
      user:  { type: string, pattern: "^[A-Za-z0-9]{30}$" }
      title: { type: string, max: 250 }
      text:  { type: string, max: 1024 }
      link:  { type: string, max: 512, optional: true }
    request:
      method: POST
      path: /messages.json
      body: { token: "{credential}", user: "{user}", title: "{title}", message: "{text}", url: "{link}" }
notify:
  call: message
  address: { param: user, env: PUSHOVER_USER }   # the person's own key, read from the user's env_file
  title: title                                   # yardr's value -> the call's parameter
  text: text
  link: link
```

What it says: one call, `message`, a `POST` to
`https://api.pushover.net/1/messages.json` with a JSON body of `token`,
`user`, `title`, `message` and, when there is a link, `url`. The `notify`
key makes it a source a user can be told through: the call to make, the
parameter that takes the person's address and the variable that address is
under in *their* file, and which parameters take the title, the text and
the link yardr has. The `max` values are Pushover's own limits; yardr cuts
a title or text that is longer and leaves out a link that is. `yardr source
--help` has the whole format.

As the operator:

```sh
yardr source add pushover.yaml
yardr source key pushover           # PUSHOVER_TOKEN, typed unseen
yardr source show pushover
```

`source show` prints the call and, for the credential, the variable's name
and that the file sets it. It never prints the value:

```
pushover  https://api.pushover.net/1
  A push to a person's phone through Pushover.
  credential: PUSHOVER_TOKEN is set in /home/mckean/.yardr/sources/pushover.env
  check: the file names no call for it
  call message  update  POST /messages.json  One push to one person
    user  string  matching ^[A-Za-z0-9]{30}$
    title  string  at most 250 characters
    text  string  at most 1024 characters
    link  string  at most 512 characters  optional
  notify: the call message tells a user, at the PUSHOVER_USER of their env_file (yardr user set <name> notify=pushover)
  added 10-07 01:08 by mckean
```

The user themselves or the operator:

```sh
yardr user key mckean pushover      # PUSHOVER_USER, typed unseen
yardr user set mckean notify=pushover
```

The second answers `user mckean: told outside the yard: through pushover
(PUSHOVER_USER is set in …/mckean.env)`. A file you keep yourself is named
instead with `yardr user set mckean env_file=<file>`, as a source's is
with `yardr source set pushover env_file=<file>`.

Then step 4's test message and step 5's subscriptions above.

## Checked against Pushover's API page

Read on 7 October 2026 at pushover.net/api: messages are a `POST` to
`https://api.pushover.net/1/messages.json`; parameters are taken
percent-encoded or as "JSON with a Content-Type of application/json", which
is what yardr sends; `token`, `user` and `message` are required, `title`
and `url` optional; a message is at most 1024 characters, a title 250, a
URL 512; tokens and user keys are 30 characters of `[A-Za-z0-9]`. A valid
request is answered 200, an invalid one 4xx ("repeating your same request
will not work"), and on anything else it may be repeated, no sooner than 5
seconds later. yardr's rule fits that: 200 is taken, 4xx is final, the
rest is tried again after 30 seconds or more.

## Trying it without Pushover

For someone working on yardr itself: `contrib/isolated-yard` has a stand-in
that takes messages as Pushover does. In an isolated yard D, with the
stand-in on the address A it printed, the by-hand file above runs as
written with two changes: `base: http://A/1` in the file (plain http is
allowed to this machine only), and `PUSHOVER_TOKEN=push-standin` in the
source's env file. `curl http://A/messages` lists what it took. See
[contrib/isolated-yard/README.md](../contrib/isolated-yard/README.md).
