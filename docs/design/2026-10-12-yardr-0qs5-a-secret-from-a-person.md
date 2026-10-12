# A person hands the yard a secret: an ask whose answer is written to a file by the server, and shown to nobody

Design yardr-0qs5, proposed 2026-10-12. A proposal as it was decided on, not a description of what the code does today; the decision is on the bead.

A yard is one installation of yardr: its store, its crew and its depots. A user is a person of a yard; the operator is the user who runs it; a crew member is one of the yard's always-on agents, and the yardmaster is the one that coordinates; a bead is one durable piece of work; an ask is one question as data on a bead, and an answer is what one person did with it; a source is a service outside the yard, declared by one file; a peer is another yard this one is linked with. docs/glossary.md has all of yardr's words. footplate is the native window over yardr's socket in which a person reads mail and picks. In this document **the value** is the secret itself (an API key, a token), **a name** is the name of the environment variable it is kept under, and **an env file** is a file of `KEY=VALUE` lines of mode 0600, which is where yardr keeps every such value today.

## 1. The question

mckean, 11 Oct 2026, after adding the source artificialanalysis had taken four commands typed from an agent's text, one of them `echo 'ARTIFICIALANALYSIS_API_KEY=…' > ~/.yardr/sources/artificialanalysis.env`: "I think the whole env communication submission could be handled in a more easier way, perhaps somehow via yardr so footplate could even be a way to communicate this in a secure way where it's not exposed to an llm". And again by mail from airy, 12 Oct 2026 18:52: "a way to ask for env variables in a secure way, this could be handled by footlpate as well or in cli chat. an agent could send a request with a value that will then go to the right place once "sent"." So: how does a crew member ask a person for a credential, and the person give it, so that the value reaches only the yard's server and the file it keeps, and no pane, mail, event, brief or bead ever holds it?

## 2. What exists today

### 2.1. Where a value is kept, and who names the file

Every value yardr uses is in a file a person wrote by hand, and the store keeps the file's path and never its content. There are five places.

| whose | set with | what names the variable | who reads the file, and when |
|---|---|---|---|
| a declared source's credential | `yardr source set <name> env_file=<file>`, or `--source-env <source>=<file>` on `yardr pack apply` (docs/packs.md) | the source's declaration: `credential.env`, `credential.update_env`, or the inputs under `credential.mint` (docs/integrations.md) | the server, for one call (`ReadCredential` in internal/source/credential.go, called from internal/store/sourcecalls.go); "keeps it nowhere, so it is in no pane's environment, no brief and no event" |
| a user's address at a notify source | `yardr user set <user> env_file=<file>` | the declaration's `notify.address.env` (for Pushover `PUSHOVER_USER`) | the server, at each send (docs/users.md) |
| a crew member's outbound credentials | `yardr crew set <name> env_file=<file>`, or `env_command=<program>` | nothing: every line of the file is loaded | the runner, when it starts the pane: the lines are "joined to the agent's pane environment" (docs/crew.md, "Outbound credentials") |
| a group's | `yardr group set`, the same two settings (docs/groups.md) | nothing | the same, for each session of the group |
| a hook's signing secret | `yardr hook add <name> --secret-file <file>` | nothing: the file holds the one secret | the hook receiver, on every delivery (`ReadSecretFile` in internal/config/envfile.go) |

One rule holds all of them, in one function: `openPrivate` in internal/config/envfile.go refuses a file that is not regular or that anyone but its owner can read or write. The docs' examples keep the files in the yard's home, beside the store and not in it: `~/.yardr/sources/<name>.env`, `~/.yardr/users/<user>.env`. docs/crew.md says why a value is never in the store: "BDP serves the store, backups copy it and every builder can read it." `env_command` is the way an outside store holds the values instead of a file; docs/crew.md's own examples are `varlock load --format env` and `op inject`.

Nothing in yardr writes any of these files. Every one of them begins as the `echo … >` of section 1.

### 2.2. Whose these settings are

`SetSource` in internal/store/sources.go is guarded by `maySource`: a change to a source "is the operator's", and "a call from the pane of a bead's session or of a crew member is that session's or that crew member's, whatever actor it names". The store knows the pane because the socket records the pid at the other end of each connection (`WithCaller` and `paneSession`, `paneCrew` in internal/store/caller.go). `mayTell` in internal/store/usernotify.go is the same check for a user's `notify` and `env_file`, which are that user's own or the operator's. `SetSource` writes an event `source_set` whose data holds the source and the path.

So an agent cannot set a credential today, and should not: `maySource`'s comment gives the reason, "whoever may replace its file can point the next call, and the credential with it, at an address of their own".

### 2.3. The line that is no wall

docs/integrations.md, on the server making a source's call: "It is a line, not a wall: every agent runs as the same OS user and could read a 0600 file if it went looking." docs/crew.md and docs/users.md say the same of their checks, and `notOperator` in internal/store/users.go says it of the actor: "a line against mistakes, not a wall, and moves when yardr hosts more than one person".

### 2.4. Asks, and everything an answer touches

`yardr bead ask <id> "<question>" --to <user> --context … --option …` stores a note of kind `ask` on a bead; its text is JSON (`Ask` in internal/store/asks.go). An answer is a note of kind `answer` (`Answer`: a pick, words, Later or Decline), written by `AddAnswer`, the one implementation behind `yardr bead answer` and the socket's `AnswerAsk`, `DeferAsk` and `DeclineAsk`. An ask with no options "wants words". This is what an answer's text reaches today, each checked in the code:

- **The note.** It is read by `yardr prime --bead`, `yardr review`, `yardr chat`, the web view, footplate (`Bead` in internal/store/decisions.go), BDP and a backup of the store.
- **The event.** `addNoted` in internal/store/graph.go writes a `noted` event with the note's seq, and `answerOf` in internal/store/yardrsource.go adds the answer as a sentence to that event's data (`e.Data["answer"]`), "so the inbox and the notification read it without another lookup". The asker is told of it without subscribing (`ReasonAnswered`).
- **Another yard.** An ask addressed `--to name@peer` is queued for the peer, and the answer comes home as a message that lands as an answer note (design yardr-h039, built: `answerForeign`, docs/peers.md). Design yardr-r6jh, approved today, has an ask for a user also sent to each name a link trusts as that user.

Three clients show a person their asks. `yardr chat` shows them as numbered text above a crew member's conversation and has no keys for them: "It shows the questions where the asker is, and the terminal answers" (docs/chat.md). Its one input is a `textarea` of the bubbles library (internal/chat/chat.go). footplate lists them as decision cards from `UserAsks` and answers with `AnswerAsk` (src/api.rs and src/window.rs in the footplate depot); its input is `Line` in src/input.rs, and text typed and not sent is kept for each conversation in src/draft.rs. The web view is read-only until design yardr-8wxe's bead yardr-bvt3.

### 2.5. Fields that do not echo

`golang.org/x/term` is a dependency already (internal/cli/init.go and four other files), and so are `huh` and `bubbles`, whose forms `yardr setup`, `yardr crew add` and `yardr pack apply` use (`huh.NewInput`, `huh.NewConfirm` in internal/cli). Nothing in yardr reads a value without echo today: no command takes a secret, they take a path to one. `yardr setup` is a run of forms for the first depot and asks for no credential.

### 2.6. The decide stage

The stage `decide` of a flow is where a bead waits until a person moves it (docs/flows.md). What the person says there is a note, an ask's answer, or the move itself. It carries no field of its own.

### 2.7. The socket, and what is planned around it

The socket is a file of mode 0600 in a home of mode 0700 and serves every method of the store (`handler` in internal/rpc/server.go); `Calls` in internal/rpc/api.go lists the 21 that docs/api.md documents for a client that is not yardr. Three designs of today bear on this one:

- **yardr-8wxe** (a yard as a service, decided): "the first secret a hosted customer hands over is an API key for a harness or a token for a tracker, the person answers in a browser, and a subscription's login is not such a secret". Its web view, started `--as <user>`, will take that user's writes.
- **yardr-r6jh** (one person across yards, approved): rule 4, "Nothing that acts follows. A move, a release, an operator's command, a setting, a secret: each is done in a yard, by an actor of that yard, under that yard's checks", and in its table of what must not follow: "A secret (design bead yardr-0qs5): nothing crosses".
- **yardr-xpf8** (a remote footplate, a draft on branch `yard/yardr-xpf8`, not decided): the 21 calls of `Calls`, and only those, served to a device on the yard's listener behind a credential. Its section 3.8 leaves this to the design in hand: "A secret typed into footplate crosses the network: not designed here. That design gets this as input: over the listener a value travels under the operator's TLS and no other protection."

A socket forwarded over ssh (`ssh -N -L <local path>:<remote socket> <host>`, design yardr-h039 section 3.1) and `ssh -t <host> yardr chat` both reach a yard from another machine today, with ssh as the whole of the trust and of the protection on the way.

## 3. The proposal

### 3.1. In one paragraph

The request is an ask, and the answer is not an answer note. An ask gains one field that says where its answer goes, by source and by name. A client shows such an ask with a field that does not echo. What the person types goes over the yard's socket in one new call; the server writes it into the env file of the thing the ask named, at a path the server chooses, and then records an answer that holds the names and never the value. Everything else an ask has stays as it is: the bead it is on, who it is for, the card in `yardr chat` and footplate, Later and Decline. The first step covers a declared source's credential, typed in `yardr chat` on the yard's own machine.

Open question 1 is whether an ask is the right carrier. This section is written as if it were.

### 3.2. Asking

```text
$ yardr bead ask yardr-k3vd "The API key for Artificial Analysis" --secret artificialanalysis \
    --context "Create one at https://artificialanalysis.ai/account/api (free plan, read only)." \
    --context "The source is added and granted to me; it has no credential yet."
ask 2650 on yardr-k3vd for mckean: a secret, ARTIFICIALANALYSIS_API_KEY of the source artificialanalysis
```

- `--secret <source>` names a declared source of this yard. The names come from its declaration and from nowhere else: every variable its credential names (`credential.env`, `credential.update_env`, or the inputs under `credential.mint`). `--env <NAME>` narrows the ask to some of them and is refused for a name the declaration does not have. **The asker never names a path, and never invents a name.**
- The ask is stored as any ask, with one field more: `"secret": {"source": "artificialanalysis", "env": ["ARTIFICIALANALYSIS_API_KEY"]}`.
- A secret ask takes no `--option`, no `--recommend` and no `--about`. It needs `--context` as every ask does: where the person gets the value.
- It is refused, storing nothing: for a source the yard does not have; for a source whose declaration names no credential; when `--to` is anyone but the operator (open question 3); when `--to` is `name@peer` (open question 6).
- A crew member with no bead for it uses `yardr bead ask --new`, where design yardr-r6jh's section 3.5 is built; until then the ask stands on the bead the work is about.

Asking gives the asker nothing. The source must exist already, added by the operator, and what the asker may call of it is still the operator's grant.

### 3.3. What the person sees and types, in `yardr chat`

```text
ask 2650 · open · to mckean · asked by yardmaster · 1m ago
The API key for Artificial Analysis
  Create one at https://artificialanalysis.ai/account/api (free plan, read only).
  The source is added and granted to me; it has no credential yet.
  a secret: ARTIFICIALANALYSIS_API_KEY, for the source artificialanalysis (https://artificialanalysis.ai)
  it goes to a file only the yard's server reads; no agent is shown it
  ctrl+o types it here · or in a terminal: yardr bead answer yardr-k3vd 2650 --secret
```

The line `a secret: …` is the server's, made from the source's declaration (its name and the host of its `base`, where its calls go), never from the asker's words: whatever the context says, the card states truly which variable of which source the value will be kept as.

`ctrl+o` on a conversation that shows an open secret ask replaces the message input with one line (the key is free today: the view binds `ctrl+c`, `ctrl+g`, `tab` and `enter`, and the text area's own keys do not include it):

```text
ARTIFICIALANALYSIS_API_KEY for artificialanalysis  ••••••••••••••••••••••••
enter sends it to the yard · esc leaves, and nothing is sent
```

The field is a `textinput` of the bubbles library with its echo set to password, so the terminal is sent dots and the screen of a pane holds dots. An ask with two names asks for one after the other and sends them together. Enter sends; the field is emptied and closed whatever the yard answers; the view then shows one line, the server's:

```text
mckean gave ARTIFICIALANALYSIS_API_KEY · it is set in ~/.yardr/sources/artificialanalysis.env · yardr source check artificialanalysis
```

The typed value is never put in the message `textarea`, never in a notice and never in an error. This is the only key `yardr chat` gets for an ask: a pick is still answered in a terminal, as docs/chat.md says.

The terminal form, for a person without the view and for a script:

```text
$ yardr bead answer yardr-k3vd 2650 --secret
ARTIFICIALANALYSIS_API_KEY for the source artificialanalysis (nothing is shown as you type):
mckean gave ARTIFICIALANALYSIS_API_KEY · it is set in ~/.yardr/sources/artificialanalysis.env
```

It reads from the terminal without echo (`term.ReadPassword`). When standard input is not a terminal it reads `NAME=value` lines from it, which is how a script or a service's provisioning answers. The value is never an argument and never an environment variable of the command: both can be read by every process of the same OS user while the command runs.

### 3.4. What the server does with it

One new method of the store, `GiveSecret(bead, ask, values, actor)`, where `values` maps a name to a value. In order:

1. **It refuses** unless the ask is on that bead, is a secret ask, is open or put aside, and the names given are exactly names the ask asked for. It refuses an empty value and one that holds a line break.
2. **It refuses whoever may not set that credential today.** For a source that is `maySource`, unchanged: the actor is the operator, and a call from the pane of a bead's session or of a crew member is refused whatever actor it names. So the call authorizes nothing that `yardr source set <name> env_file=` does not authorize now.
3. **It writes the file.** If the source has an `env_file`, that file: the line of each given name is replaced or added, and every other line and comment is kept. If it has none, `<home>/sources/<name>.env`, and the source's `env_file` is set to that path in the same write, with the `source_set` event that setting it has today. The file is written whole beside itself with mode 0600 and renamed into place, so a reader sees the old file or the new one. A file that `openPrivate` would refuse (not regular, open to others) is refused here too, and not repaired.
4. **It records the answer**: a note of kind `answer` whose JSON is `{"ask": 2650, "given": ["ARTIFICIALANALYSIS_API_KEY"]}`. The sentence the event and the asker's inbox carry is made from that: `mckean gave ARTIFICIALANALYSIS_API_KEY for the source artificialanalysis`.
5. **It answers the caller** with the words `yardr source show` prints for a credential's state (`CredentialState`: "… is set in …"), which never hold a value.

Giving again is allowed while the ask is not declined, as a newer pick replaces an older one: a mistyped key is typed again, and each time is a note with the names.

What the rest of an ask must learn, so that no other path takes a value:

- `AddAnswer` refuses a pick and refuses words on a secret ask: `ask 2650 takes a secret, not words: yardr bead answer yardr-k3vd 2650 --secret`. The refusal does not repeat what was sent. This is what holds a client that does not know the field yet, which would show the ask as "words wanted": the words reach the server and are dropped.
- Decline on a secret ask takes no words, for the same reason. Later is unchanged.
- `Lines` (prime, review, chat) and `Bead`, `UserAsks` (footplate, the web view) render the ask with the server's `a secret:` line and the answer as `gave <names>`.
- `GiveSecret` is documented in docs/api.md for footplate, in a list of its own, `LocalCalls`: calls that are served on the socket and on nothing else. Design yardr-xpf8's handler serves "exactly the 21" of `Calls`; this keeps it so, and open question 5 is whether that ever changes.
- The call's arguments are logged nowhere. internal/rpc logs no request body today; a test holds it for this call.

**One store function does the writing**, `writeEnv(file, values)`, and `GiveSecret` is its first caller. Its second is a command for a person nobody asked: `yardr source key <name> [NAME…]`, which reads the values without echo and gives them under the same check. It replaces the `echo … >` of section 1 for an operator who adds a source by hand, and it is the whole of open question 1's option C.

### 3.5. What an agent in a pane sees, step by step

| step | what the asker sees | what any other agent sees | where the value is |
|---|---|---|---|
| the ask is written | its own command and `ask 2650 on yardr-k3vd …: a secret, ARTIFICIALANALYSIS_API_KEY of the source artificialanalysis` | in `yardr prime --bead yardr-k3vd`: the question, the context, the name and the source | with the person |
| the person reads the card | nothing | nothing | with the person |
| the person types it, in their own terminal or in footplate | nothing. Its pane is not where it is typed. A pane that showed the chat would hold dots | nothing | the person's keyboard or clipboard; the memory of `yardr chat` or footplate; the socket (a file of mode 0600); the server's memory for the length of one write; the env file |
| the server has written it | an inbox line: `mckean gave ARTIFICIALANALYSIS_API_KEY for the source artificialanalysis (ask 2650 on yardr-k3vd)` | the event stream: `noted` with the note's seq and that sentence; `source_set` with the file's path if the file is new | the env file, mode 0600, in the yard's home |
| the asker checks | `yardr source check artificialanalysis`: the name, the file it is set in, and how the declaration's check call ended | the same, if it asks | the env file; the server's memory for the length of one call |
| the asker uses the source | `yardr call artificialanalysis …`: what the service answered, if the operator granted it the call | the same under its own grant, else a refusal | the same; and the request the server sends |

The value is in none of: a note, an event, a mail, a brief, a prime, a blob, the store file, a backup of the store, BDP, the web view, a peer's outbox, the log, a command's arguments, an environment, a terminal's scrollback.

What an agent can still do is in section 3.6, and it is what it can do today.

### 3.6. The line this does not move

Every agent runs as the OS user the server runs as. An agent that runs `cat ~/.yardr/sources/artificialanalysis.env` reads the key, after this design as before it. What this design gives is that **no agent is shown the value without going looking for it**: the person no longer types it in a shell an agent's text told them to open, with the key one paste away from the agent's pane, and it is in nothing an agent reads in the course of its work.

Two things follow from the same fact and are said here so that nobody is surprised by them.

- An agent that leaves its pane's process tree (a process it starts that outlives its shell) is not known by its pane, and can call `GiveSecret` as the operator. What it gains is to replace a credential of a source with a value of its own, never to read one. `yardr source set <name> env_file=` has this weakness today; the check is the same line.
- For a crew member's or a group's env file (open question 2) the value is put in the agent's environment on purpose: that is what the file is for. "Not shown to an agent" then means only that it was not in the conversation.

A wall would take one of the following. Open question 4 is whether to build one.

| | what it is | what it holds | what it costs |
|---|---|---|---|
| the macOS Keychain | the value is a keychain item that only the yardr binary may read | an agent's `cat` | nothing on Linux, where the service's yards would run; an item is tied to the binary that made it, and yardr's binary is replaced at every install, so each install asks the person again in a window; an agent may still run `yardr` itself, which is the binary that may read |
| a second OS user | the server, or a small part of it that reads credentials and makes the sources' calls, runs as another user who owns the files | an agent's `cat`, for the credentials the server uses | root at install; two users and the permissions between them for the socket, the home and the depots; and nothing for a crew member's env file, which must reach the pane |
| a proxy that holds the values | an agent's environment holds placeholders, and a proxy on the way out puts the values in (varlock's credential proxy does this) | a credential an agent uses in its own pane | a proxy to run and to trust with every request; `env_command` already lets such a tool be the store, and yardr builds none of it |
| a machine for each yard | design yardr-8wxe's shape: one customer, one machine | one customer's agents from another's values | it is no wall inside the yard |

### 3.7. From another machine

- **Over ssh, with nothing built.** `ssh -t mac yardr chat` is the view of section 3.3 on the yard's own machine, typed from anywhere. A footplate started on a socket forwarded over ssh calls `GiveSecret` on the yard's own socket. The value travels inside ssh. This is how mckean, who sits at airy while the yard works on the Mac, answers from the first step on.
- **Across a link: nothing.** A secret ask is not sent to a peer and is not copied to a joined name. `bead ask --secret --to name@peer` is refused, and design yardr-r6jh's copy leaves a secret ask out. That is r6jh's table as approved. The asker's mail and, with r6jh's bead 4, the phone tell the person that a question waits; the person answers at the yard that asked. Open question 6 has the other options, and says what the yardmaster's "sealed to the receiving yard's key" would contradict.
- **Over a listener or the web view: not in the first step.** Open question 5.

### 3.8. Care

| what | what stands against it |
|---|---|
| The asker's words lie about what is asked ("paste your GitHub password") | The card's `a secret:` line is the server's, from the declaration: the name, the source and the host its calls go to. The value can only become that variable of that source, which the operator added |
| An agent asks for a key it should not have | Asking gives nothing. The value goes to a source the operator added, and is used only in calls the operator granted |
| An agent answers its own ask, to swap a credential | `maySource`, by the pane (3.4, step 2), and the limit of 3.6 |
| A client that does not know the field shows "words wanted" | The server refuses words and picks on a secret ask and keeps nothing (3.4). footplate's bead F1 lands before the trial on the real yard |
| The value is shown by mistake later | No command prints a value: `source show` and `source check` print names and the file, and `ReadEnvFile`'s errors "name the file and a line number, never a value" |
| The person pastes the wrong thing | The view says which name was set and offers `source check`; giving again replaces it. A value sent to a service in a failed check is a wrong key sent to the right host |
| Text from outside is used as a path | Nothing the asker or the answerer sends is a path. The source's name is one the store holds, and the file is the one already set or `<home>/sources/<name>.env` |

## 4. Left out

- **A new noun**, `yardr secret ask`, with a record, a queue and commands of its own. An ask has the queue, the card, the address, Later, Decline and, with design yardr-r6jh, the phone. Open question 1 keeps it as an option.
- **The value in the store**, encrypted or not (mckean's thought of 11 Oct 2026, 23:35). The store is what BDP serves, backups copy and a peer's snapshot reads; a value in it needs a key that lives somewhere else again, which is a file. The bead's note of that evening leaves it to the PRD on YRD-27, and so does this design.
- **A credential that is a person's**, picked by the server for the user the work is done for; **a grant on a bead**; **a key minted for one bead**. They are the bead's notes of 11 Oct 2026 and the PRD on YRD-27. This design is the handing-over form they would all use: none of them changes how a value gets from a person to a file.
- **yardr as a store of secrets**, or a dependency on varlock or 1Password. `env_command` already lets either be the store for a crew member or a group. Where one is set there is no file for the server to write, and a secret ask for that crew member or group is refused, saying so.
- **A hook's signing secret and a user's notify address** as things to ask for. A hook's secret is chosen together with the webhook in the service, by the person who runs `yardr hook add`, and a user's address is no secret. Both are a file of the same rule, so the same writer could take them later.
- **A login.** A subscription's login is done in the harness's own screen (design yardr-8wxe, section 2.10: the terms forbid collecting it). A secret ask is for a key or a token, and its card never says "password".
- **Checking the value as it is given.** The server does not call the service inside the write. `yardr source check` exists, and the answer line names it.
- **Reading a value back**: no call returns one, to anyone.

## 5. Open questions

**1. What carries the request?** Option A, an ask with a field for where its answer goes, as proposed: the queue, the cards, the address and the phone exist; it costs a kind of ask whose answer is not a note with the answer in it, three refusals in `AddAnswer`, and one more call for clients to learn. Option B, a noun of its own (`yardr secret ask`, the yardmaster's first shape): nothing about asks changes; it costs a second queue in `yardr chat`, footplate and the web view, with its own Later, Decline and telling. Option C, no request at all: only `yardr source key <name>`, and an agent that needs a key mails the person the command; it is the smallest, it ends the `echo … >`, and it costs that nothing waits in the person's decisions and that footplate cannot answer. I would take A. C is built on the way to it (bead Y1) and is worth having by itself.

**2. What may be asked for?** Option A, a declared source's credential only, as the first step has it: the server is the only reader, and the names come from a declaration. Option B, also a crew member's and a group's env file (`--secret-for crew:yardmaster --env LINEAR_API_KEY`), which is where design yardr-8wxe's "API key for a harness" and a tracker's token for a built-in source go: it costs a name the asker chooses, since no declaration names it, and a value that is put in an agent's environment by design (3.6). Option C, B and also a hook's secret and a user's address. I would take B, as a bead of its own after the trial (Y4), with the card saying in words that the value will be in that crew member's environment.

**3. Who may be asked, and who may give?** Option A, the operator only, as proposed: it is `maySource` unchanged, and a yard has one person who decides where its credentials go. Option B, any user of the yard, for a source: a second person can hand over a key; it costs the first setting of a source that someone other than the operator may change. I would take A. A credential that is one user's own is the PRD's question, not this one.

**4. Does the design stop at the line?** Option A, yes: "no agent is shown the value without going looking", said in those words in docs/integrations.md, as proposed. Option B, the macOS Keychain where there is one. Option C, a second OS user for the server's credentials. Section 3.6 has what each holds and costs. I would take A, and take up C only together with the PRD's grants: a wall around a file is worth its cost once who may use a credential is something the yard enforces.

**5. May a value come in over a network?** Option A, no: the socket only, which over ssh is already every machine mckean works from; `GiveSecret` is in `LocalCalls` and stays there. Option B, also through the web view started `--as` the operator, once yardr-bvt3 lets it answer asks: it is what a hosted customer without a terminal needs (design yardr-8wxe), and it costs a value that crosses the edge's proxy in the clear after its TLS ends, and a web view process that passes it on. Option C, also through design yardr-xpf8's listener, for a device of the operator's: under the operator's TLS and no other protection, as that design says, and a stolen device token could then replace a credential. I would take A for the first step, B as bead Y5 when yardr-bvt3 has landed, and not C before yardr-xpf8's trial has been lived with.

**6. May a secret ask cross a link?** Option A, nothing crosses, as proposed and as design yardr-r6jh approved it: the person is told by mail or phone and answers at the yard that asked, over ssh. Option B, the question crosses and the value does not: the card stands in the person's decisions at the other yard with no field and the line "answered at mac: ssh -t mac yardr chat"; it costs a word both builds must know, so that an older yard is never sent a secret ask it would show as "words wanted". Option C, the value crosses, sealed to the asking yard's key, under a word of its own on the link, off by default (the yardmaster's thought 4): the person answers where they sit; it costs the value in the clear in a second yard's footplate and server, a sealed copy in an outbox for up to a day, and a link through which the other yard's word changes a credential here, which is what r6jh's rule 4 and its table say must not follow. I would take A now and B when he misses it. I would not take C: design yardr-xpf8's direct window is the better way to act in a yard from far away, since the person then acts in that yard as its user.

**7. Does the same card carry the operator's yes to adding and granting the source?** Option A, no, as proposed: `yardr source add` and `yardr source grant` stay commands in the operator's terminal, or one `yardr pack apply` with its plan where a pack brings the source. Of the four commands of 11 Oct two are left, and neither holds a secret. Option B, yes: an ask whose yes runs the add and the grants, shown with the declaration as `pack apply` shows a plan; one card for the whole of that afternoon; it costs the first answer in yardr that acts as the operator, on a file an agent wrote that says where a credential is sent. I would take A, and make B a design of its own if two commands still hurt after the trial.

## 6. The beads it would become

In the order they would land. The first step is Y1, Y2, Y3 and the trial T. Each yardr bead passes `sh .yardr/test` and updates the pages named. No bead's own tests start an agent: they use a store in a temporary home. The trial does, and says how.

**Y1. The server writes a source's credential, and `yardr source key` types one.** Goal: section 3.4's steps 2, 3 and 5 without an ask. Where to look: `SetSource` and `maySource` in internal/store/sources.go; `openPrivate`, `parseEnv` and `ReadEnvFile` in internal/config/envfile.go for the rule and the format the writer must keep; `CredentialState` and `Names` in internal/source/credential.go; the `set` command in internal/cli/sourcecmd.go; `term.ReadPassword` as internal/cli/init.go imports it. Done when: a store method takes a source and a map of names to values and writes them as 3.4 says, into the source's `env_file` or, with none set, into `<home>/sources/<name>.env` with `env_file` set in the same write; a name the declaration does not have, an empty value and a value with a line break are refused; other lines and comments of the file are kept; the file is replaced by rename and is 0600; a file open to others is refused; it is refused for anyone but the operator and from an agent's pane, in `maySource`'s words; `yardr source key <name> [NAME…]` reads without echo from a terminal and `NAME=value` lines from a pipe, and prints the state line; a test gives a value and then finds it in the env file and nowhere in the store file, the events or the server's log; docs/integrations.md has the command and says the line of 3.6. Depends on open question 3.

**Y2. An ask that takes a secret.** Goal: sections 3.2 and 3.4. Where to look: `Ask`, `Answer`, `AddAsk`, `AddAnswer`, `answerShape`, `answerable` and `lines` in internal/store/asks.go; `answerOf` in internal/store/yardrsource.go for the sentence on the event; `Bead` and `UserAsks` in internal/store/decisions.go; the `ask` and `answer` commands in internal/cli/beads.go; `Calls` and the page's test in internal/rpc/api.go; the queueing for a peer in `AddAsk` for the refusal of `name@peer`. Done when: `yardr bead ask --secret <source> [--env NAME]` stores the ask with the field and prints 3.2's line, and is refused in each case 3.2 lists; `GiveSecret` does 3.4's five steps with Y1's writer; a pick, words, and Decline with words are refused on a secret ask without repeating what was sent; giving again replaces; prime, review, `Bead` and `UserAsks` show the server's `a secret:` line and `gave <names>`; the asker's inbox line is 3.5's; `yardr bead answer <id> <ask> --secret` reads as `source key` does; `GiveSecret` is in docs/api.md under a list `LocalCalls`, held by the page's test, and `Calls` still has 21; the test of Y1 for where the value is runs through `GiveSecret`; docs/chat.md and `yardr bead ask --help` say it. Depends on Y1, and on open questions 1 and 6.

**Y3. `yardr chat` types a secret.** Goal: section 3.3. Where to look: `Model`, `Update` and the `textarea` in internal/chat/chat.go; the cards in internal/chat/view.go; `Options.Asks` and internal/chat/asks.go; the `textinput` of bubbles and its echo mode. Done when: `ctrl+o` on a conversation with an open secret ask opens the field, and says there is nothing to give where there is none; the model's view never holds a typed character, held by a test that types a value and searches every frame; enter calls `GiveSecret` and shows the server's line, esc sends nothing; a refusal is shown without the value; the card's last line names the key and the terminal command; docs/chat.md has the key in its table. Depends on Y2.

**T. The trial: one key typed in `yardr chat` on the yard's own machine.** First in an isolated yard (`contrib/isolated-yard/isolated-yard up`, every command through its `D/e`), with its stand-in source `crm` (contrib/isolated-yard/sources/crm.yaml): ask, type, check, call. Then on the real yard, with the next source that needs a key, mckean typing it through `ssh -t mac yardr chat`. Its note on this bead says: what the yardmaster's pane held at each step, by reading the pane back; whether two commands for add and grant still hurt (open question 7); and whether he wanted to answer at airy (open question 6). Depends on Y3.

**F1 (footplate). The card of a secret ask, and a field that shows dots.** Goal: section 3.3 in the window. Where to look: `answer_ask` and `call` in src/api.rs for the new call; the decision cards in src/window.rs and src/model.rs; `Line` in src/input.rs, which gains a masked mode; src/draft.rs, which must never hold the field's text; src/fake.rs for a fake yard that takes `GiveSecret`. Done when: a secret ask's card shows the server's line and a masked field in place of the words line; paste works and nothing copies out; Enter calls `GiveSecret` and the field is emptied whatever is answered; the value is in no draft, no setting and no notice, held by a test; against a yard that does not know the call the card says to answer in a terminal. Depends on Y2; it lands before the real half of T if mckean answers in footplate.

**Y4. A crew member's and a group's env file take a secret.** Only if open question 2 is answered with B or C. Goal: `--secret-for crew:<name>|group:<name> --env <NAME>` on `bead ask`, written by Y1's writer to that env file or to `<home>/crew/<name>.env`. Where to look: `PutCrew` and its `operatorOnly` in internal/store/crew.go; the group's settings in internal/store; internal/config/crewenv.go; docs/crew.md, "Outbound credentials". Done when: the card says in words that the value will be in that agent's environment from its next start; a crew member or group with `env_command` is refused, naming the command; the names are held to `envKey`; the check is the one `crew set env_file=` has. Depends on T.

**Y5. The web view takes a secret.** Only if open question 5 is answered with B, and after yardr-bvt3. Goal: a secret ask's form in the view started `--as` the operator has a password field, sent by POST, never in a URL, with the browser's saving and completion turned off. Where to look: yardr-bvt3's form for an ask; internal/web. Done when: the form gives as `GiveSecret` does; a view started from an agent's pane is refused by the pane check, held by a test; docs/web.md says what the value crosses on the way. Depends on Y2 and yardr-bvt3.

Not made here: a bead for a secret ask across a link (open question 6), for the listener of design yardr-xpf8 (open question 5), and for a wall (open question 4). Each is its own design step if its question is answered that way.
