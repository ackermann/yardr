# Weighty cards, a gate in front of them, and one view of every grant

Design yardr-ekmx, proposed 2026-10-21. A proposal as it was decided on, not a
description of what the code does today; the decision is on the bead.

## 1. The question

The yardmaster listed the risks of the Google walk (yd-c546), and mckean
answered on 17 Oct at 19:00: "1 and 2 are real and we could tag those so
they can be visually distinguished we can also consider password/auth
gates." Risk 1 is approving on reflex: about twenty cards came in twenty
minutes in that walk, and nothing set apart the few that gave a credential,
let an agent send mail as a person or brought a program to run, from the
many that changed nothing much. Risk 2 is that credentials and grants
spread, each reviewed alone on its own card, and nowhere is the whole seen.
The bead asks four things settled: which cards are weighty and who decides
it, how the weight shows on every surface, whether an authentication gate
stands in front of a weighty approval, and one view of what all the grants
add up to.

A *card* here is an ask a person answers by approving rather than by
picking words: a run ask (it proposes one yardr command, `Ask.Run`), a
setup ask (a pack's whole setup, `Ask.Setup`), a secret ask (a value the
person types, `Ask.Secret`) and a mapping ask (who an unmapped sender is,
`Ask.Identity`). *Weighty* is the word this design gives a card whose
approval widens what reaches outside the yard, whose word or credential
counts as a person's, or what runs inside the yard. Everything else is
*routine*.

## 2. What exists today

**The cards and who writes their words.** `internal/store/asks.go` holds
`Ask` with its four card shapes (`Secret`, `Run`, `Setup`, `Identity`). A
run ask's command is one row of `runList` in `internal/store/runask.go`
(`runCommand`: `parse`, `check`, `card`, `stale`, `run`, `whose`, `sole`,
`link`). The writer sends `argv` alone; everything a card says (`Does`,
`Back`, `BackBy`, `Seen`, `Carried`, `Pack`) is written by the yard in the
write that stores the ask (`askedRun`), and the approval (`approveAsk`)
writes the card again and refuses as stale when it differs. Twenty-two
rows today: `peer allow`, `peer intake`, `peer identity`, `source set`
(limits only: `env_file` and `mint_command` parse but are refused by the
row's check as the operator's alone, `runask_list.go` `parseSourceLimit`,
`checkSourceLimit`), `source grant`, `source add`, `source ignore`,
`source check`, `pack apply`, `hook add`, `subscribe`, `user set`, `user
key`, `user add`, `user rm`, `crew set`, `crew identity`, `user identity`,
`settings set`, `peer rename`, `bead update --originator`, `timer rm`.
What no card may propose is `aloneList` in `runask_alone.go`.

- Secret asks: `AskSecret` in `secretask.go`, with its forms (a source's
  credential, a hook's signing secret, a waiting login's input, a harness
  API key, a user's address, a crew member's credential, a user's own
  credential `As`) and `Line()`, the yard's sentence for each.
- Setup asks: `AskSetup` in `runask_setup.go` (design yardr-7d4e): the
  pack, its presets as options, the account typed beside the pick.
- Mapping asks: `AskIdentity` in `asks.go`, written by `ClaimIdentity` from
  a dropped delivery; a pick writes the identity.
- Commands carried on a card: `AskSource.Files` (`AskFile` with program,
  text and SHA-256) and `AskSource.Old` (`AskFileDiff`) in `runask.go`.
  Since yardr-d860 a pack brings the mint of a source it declares, and
  since yardr-y976 a declared call may name a `build` program
  (`source.Call.Build`, `internal/source/declaration.go`); both are
  command files of the declaration (`Declaration.CommandFiles`,
  `internal/source/command.go`), carried and shown by digest on the
  `source add` and `pack apply` cards like the thread and normalize
  scripts.
- Call kinds: `source.Read` and `source.Update`
  (`internal/source/declaration.go`). An update call changes something in
  the service outside the yard; a read call reads.

**How a card reaches a person.** `OpenAsk`, `DecidedAsk` (in
`decisions.go`, the rows of `UserAsks`) and `BeadAsk` (the `asks[]` of
`Bead`) carry `run`, `secret`, `setup` and `identity` as the client
shows them (`RunAsked`, `SecretAsked`, `SetupAsked`, `IdentityAsked`);
`docs/api.md` documents each under `UserAsks` and `Bead`.

- footplate (`/Users/mckean/projects/repos/footplate`, Rust and gpui)
  draws one open card at a time and approves it with ⌘↵
  (`window.rs` `approve`, `send_approve`, `APPROVE_KEY`; `run_keys` says
  "enter alone approves nothing"). It has no LocalAuthentication, Touch ID
  or other gate: a grep of `src/` finds none. Where the yard does not
  serve the call to the window, the card says "this yard takes no approval
  from footplate. Approve in a terminal on the yard's machine:"
  (`APPROVE_ELSEWHERE`).
- `yardr chat` (`internal/chat/view.go` `questions`) and `yardr bead ask`
  and `yardr bead answer` (`printAsk` in `internal/cli/bead_ask.go`) print
  a card with the one renderer `ResolvedAsk.LinesMarked`
  (`internal/store/asks.go`). Chat approves
  nothing; its card's last line is the command that answers it, and the
  approval is `yardr bead answer <id> <ask> --approve`, one ask per
  command.
- `yardr web` names the approving command in its waits
  (`internal/web/waits.go`, "to approve: ...") and approves nothing.
- No surface has an approve-all. Every approval is one call naming one
  ask: `ApproveAsk`, `ApproveSetup`, `GiveSecret`, `AnswerAsk`.

**Who may approve, and from where** (`docs/api.md`, "Over the network" and
"For the operator alone"). On the socket the operator approves, never from
an agent's pane (`internal/peer` names the caller's process tree). On the
listener a device of the operator approves with its bearer token, and only
from a request that reached the listener from the yard's own machine (the
tunnel or proxy ending TLS stands there). The yard knows the caller by the
token, not by anything about the person holding it.

**The grants.** `SourceGrant` in `internal/store/sourcecalls.go`
(`Source`, `Grantee` of a group or a crew member, `As`, `Calls` holding
`read` and call names, `At`, `By`), kept under `source_grants` and read by
`SourceGrants(ctx, src)`, every source for `""`. Each grant writes a
`source_granted` event, which carries the bead and ask it was approved on.
Grants are shown piecemeal today: per source in `yardr source show`, per
group in `yardr group list` and `group show` (`internal/cli/ops.go`), per
crew member in `yardr crew show` (`internal/cli/crew.go`), and as notes in
`yardr doctor` (`internal/cli/doctorsources.go`). Nothing lists them all
with when and by whom. A grant record's `At` and `By` are those of the
last call added to it (`grant` in `sourcecalls.go` rewrites both); each
call's own time and approver are in its `source_granted` event. `SourceGrants` is on `store.API` but is not one of
the socket calls programs outside yardr may rely on (`Calls` in
`internal/rpc/api.go`, `docs/api.md`), so footplate cannot read it.

**Reminders.** `yardr timer add --every <duration> --for <duration>`
(`docs/timers.md`) tells a user something on a period.

## 3. The proposal

### 3.1 Which cards are weighty, and where the mark lives

The yard weighs a card in the write that stores it, from the card it has
just written and its own records, exactly as it writes `Does`: never from
the asker's words, options or context. A card's weight is a list of
classes; an empty list is routine. Five classes, each a widening; a card
that narrows (a revoke, a lower limit, `user rm`, `timer rm`) is never
weighty:

| class | the yard's line, as the card prints it | the cards that carry it |
|---|---|---|
| `credential` | gives a credential or an address to the yard | every secret ask (`AskSecret`, all forms); `user key`; a setup ask (it takes the account) |
| `as-user` | lets calls or words count as a person's | `source grant ... --as <user>`; a secret ask with `as`; `user identity`; a mapping ask (its picks write a user's identity) |
| `outside` | lets the yard change something outside it | `source grant` naming an update call, or a `--preset` whose calls include one; `source set <s> updates_per_hour=<n>` raising the limit; a setup ask whose presets grant an update call |
| `code` | brings a program the yard will run | `source add` and `pack apply` whose carried command files (thread, normalize, mint, build) are new, or differ from the copies that stand (`AskSource.Files`, `AskSource.Old`); a setup ask whose pack brings any |
| `trust` | widens who the yard listens to | `user add`; `peer allow` adding a kind or raising the mail limit; `crew set peers=` adding a peer; `hook add` (an endpoint that takes outside input) |

Each class's line is followed by the yard's particulars, as `Does` is:
`outside: lets the yard change something outside it: crew member
yardmaster may call send of google-mail (an update)`. A card may carry
several classes; a `source grant google-mail send --crew yardmaster --as
ada` carries `outside` and `as-user`.

The mark lives where the card's words live:

- Each `runCommand` row gains `weigh func(w *wtx, run AskRun) []AskWeight`,
  beside `card`. A nil `weigh` is a routine row. The rows above that are
  weighty in some forms (`source grant`, `source set`, `peer allow`,
  `crew set`) weigh their parsed `argv` against the yard's records, as
  their `card` already does (which calls are updates comes from the
  standing declaration; a preset's calls from the pack).
- `askedRun`, the secret ask's write, `askedSetup` and `ClaimIdentity`
  store the result on the ask: `Ask.Weight []AskWeight`, with
  `AskWeight{Class, Line}`. It is the yard's own value: `newAsk` clears
  whatever a writer sends, as it clears `Stage` and `Move`.
- The approval weighs again in its own write (`approveAsk`,
  `ApproveSetup`, `GiveSecret`, a mapping pick in `addAnswer`). Weight
  that differs from what the card showed is stale, with the stale
  refusal's words: nothing runs, the ask is declined and asked again. An
  ask written before this change carries no weight; if it weighs as
  weighty at its approval it is stale in the same way, so no weighty
  command is approved from a card that did not show it.

### 3.2 How weight shows

**The socket API.** `weight`, additive, on `OpenAsk` and `DecidedAsk`
(`UserAsks`) and on `BeadAsk` (`Bead`'s `asks[]`): an array of `{class,
line}`, absent on a routine card. `docs/api.md` names the five classes
and says a client draws any class it does not know as weighty with its
line, so a sixth class later needs no client change.

**Terminal cards** (`yardr chat`, and `yardr bead ask` and `bead answer`
through `printAsk`). The
one renderer `ResolvedAsk.lines` puts a line before the question:

```
ask 412 · open · to mckean · asked by yardmaster · 3m ago
  WEIGHTY · outside: lets the yard change something outside it: crew member yardmaster may call send of google-mail (an update)
  WEIGHTY · as-user: lets calls or words count as a person's: as ada
  Grant send of google-mail to the yardmaster, as ada?
  ...
  yardr bead answer yd-c546 412 --approve
```

drawn in `yardr chat` in the `alarm` style its theme already has for
"someone has to act" (`internal/chat/style.go`), and in plain text elsewhere.

**footplate.** A weighty card is drawn with the mark first in its head
and each class's line under it, in the design system's signal red
(`--signal-red`, `yardr-design/tokens/colors.css`), and its approve
control reads `Approve in <yard> with Touch ID` (3.3). ⌘↵ alone does not
approve a weighty card: it opens the gate, so a hand pressing ⌘↵ through
twenty cards stops at each weighty one.

**Batching.** No surface approves several cards at once today.
`docs/api.md` gains the rule for any that comes: a weighty card is
approved alone, by a call naming it, and never in a batch; a batch call,
if one is ever added to the API, refuses a weighty ask in it by name and
approves the rest.

### 3.3 The gate

What the yard can hold to itself is that whoever approves a weighty card
saw it was weighty. What it cannot see is whether a person, rather than a
program holding the operator's token or shell, pressed the button. So the
gate has two halves:

- **The yard's half, verified.** `ApproveAsk`, `ApproveSetup`,
  `GiveSecret` and `AnswerAsk` (for a mapping ask) take an optional
  argument `weighed: true`. A weighty card approved without it is refused
  with the code `error`: `ask 412 is weighty (outside, as-user): approve
  it where its weight is shown; a client sends "weighed": true once its
  person has passed its gate`. A client that has never heard of weight,
  an old footplate or a saved request on a phone, cannot approve a
  weighty card with one press, and a routine card is approved as before.
- **The client's half, enforced by the client.** The client sends
  `weighed` only after its own gate:
  - **footplate on macOS:** LocalAuthentication's
    `deviceOwnerAuthentication` policy, which takes Touch ID, a paired
    Apple Watch or the login password, with the reason `approve <command>
    in <yard>`. One gate per weighty approval, with no reuse window.
  - **footplate on Linux:** no gate. The weighty card's control is the
    decline path below.
  - **`yardr bead answer --approve` in a terminal** (and `--secret`, and a
    mapping pick): at a terminal the command prints the weight lines and
    asks the person to type the ask's number back; it sends `weighed` on
    a match and stops on anything else. With no terminal (standard input
    is not one) it is refused unless `--weighed` is given. The terminal is
    the yard's own machine and its shell is the operator's, so this is
    friction against reflex and not authentication.
  - **a phone:** there is no phone client that approves today, only a
    saved request, which now cannot approve a weighty card without
    sending `weighed` on purpose. A phone client built later gates with
    the phone's own owner authentication (Face ID) before it sends it.
- **The decline path.** A client with no gate does not offer to approve a
  weighty card. It says, in the words footplate already uses for an
  approval it cannot make: `this card is weighty and this window has no
  gate. Approve it in footplate on a Mac, or in a terminal on the yard's
  machine:` with the `yardr bead answer ... --approve` line. The card
  stays open; nothing is declined for the person.

### 3.4 One view of every grant

**The command.** `yardr source grants`, reading `SourceGrants(ctx, "")`,
with `--source`, `--crew`, `--group` and `--as` to narrow and `--json`
through `a.out`:

```
$ yardr source grants
SOURCE        HOLDER                         CALLS                        GRANTED       BY
google-mail   crew member yardmaster         read (4 calls)               17 Oct 19:02  mckean
google-mail   crew member yardmaster as ada  send (update)                17 Oct 19:04  mckean, on yd-c546 ask 412 asked by yardmaster
telegram      group builders                 read (2 calls), reply (update) 12 Oct 10:15  mckean
3 grants of 2 sources to 2 holders: 2 may change something outside the yard, 1 calls as a user.
take one back: yardr source revoke <source> <call> --crew|--group <holder> [--as <user>]
```

Each row is one `SourceGrant`, plain and `--as` grants as separate rows,
since they are separate records. `GRANTED` and `BY` are the record's,
which is when it last widened and who widened it. `read` is shown with how many read calls
it covers today, from the standing declaration. Update calls and `--as`
rows carry the same words as the weighty classes. The `on ...` part comes
from a new additive field on the grant record, `SourceGrant.On` (the bead,
the ask and its asker), written by a grant approved on a card; grants
written before it show `BY` alone.

**The socket.** `SourceGrants` joins `Calls` in `internal/rpc/api.go` and
`docs/api.md`, served on the listener to a device of the operator only,
as the operator's calls are.

**footplate's view.** A grants view listing the same rows by source, with
the weighty words drawn as on a card, and the `yardr source revoke` line
for each row to copy. It changes nothing itself.

**A review reminder.** None built in (open question 4). `docs/integrations.md`
says how to set one with what exists: `yardr timer add --every 30d --for
365d "read yardr source grants"`.

## 4. Left out

- **A gate the yard can verify** (a key in the Mac's Secure Enclave that
  signs each weighty approval, enrolled with the yard like a device). It
  would stop a stolen device token from approving a weighty card, which
  `weighed` does not. Open question 1 weighs it; the proposal leaves it
  out because the risk mckean named is reflex, and it needs an enrolment,
  a key per device and a signing path in every client, with nothing for
  a terminal.
- **Changing what any card may ask.** Weight marks; it permits and
  refuses nothing that is permitted or refused today. `source set
  mint_command=` stays the operator's alone, so it carries no class here;
  a mint reaches a card only as a pack's command file, which is `code`.
- **Weighing read grants.** A plain `read` grant is routine (open
  question 2).
- **Other standing grants in the overview:** what peer links carry (`peer
  allow`), a crew member's `open_threads`, `set_state` and `peers`,
  subscriptions, identities and devices. Each is a grant of a kind; the
  bead asked for source by grantee by call, and these have their own
  `show` commands. A later overview can add them as more row kinds.
- **When each grant was last used.** Every call writes a `source_call`
  event, but reading the last per grant means scanning the log or a new
  record per grant; the overview shows what is granted, not what is used.
- **A weighted web UI.** `yardr web` approves nothing; its waits row may
  pick up `weight` later.
- **Asks from another yard** (`bead@peer`). They are never approved here
  (`ApproveAsk` refuses them); their weight is the asking yard's.
- **A cooldown or a count of approvals per minute.** The gate in front of
  each weighty card is the brake; a cap on routine cards would only slow
  routine work.

## 5. Open questions

**1. Does the yard verify the gate, or only that the client knew?**
Option A, `weighed` alone (3.3): one argument and a refusal in the yard,
a gate in each client; it stops reflex and naive clients, and a holder of
the operator's token can still approve anything by sending `weighed`, as
they can approve anything today. Option B, a yard-verified gate: each
gated client enrols a hardware-held key (Secure Enclave on a Mac, the
phone's keystore), the yard refuses a weighty approval without a fresh
signature over the yard, bead, ask and card; it stops a stolen token, and
costs an enrolment flow, a key store in footplate, a design of its own and
no answer for the terminal. I would take A now and leave B to a design of
its own if tokens are ever carried off the yard's machine.

**2. Is a plain read grant weighty?** Option A, no (3.1): `read` of a
mailbox is routine, so the mark stays rare enough to mean something; in
the Google walk it would have marked the send and `--as` grants, the
credentials and the mint, not every read. Option B, every grant is
weighty: simple and safe, and most of a source walk turns red, which is
the reflex the bead is about. Option C, a source declares that its reads
are personal (`personal: true`) and its read grants are `as-user`: a new
field in every declaration and a judgement for each pack author. I would
take A.

**3. What gates a terminal?** Option A, typing the ask's number back
(3.3): friction against reflex, no dependency. Option B, nothing: the
terminal is the operator's shell and `--approve` is already one ask at a
time. Option C, the macOS login password through a helper: a cgo or
helper binary for a gate that a shell holding the socket can skip anyway.
I would take A.

**4. A periodic review reminder?** Option A, none built in; the docs show
the `yardr timer` that sets one (3.4). Option B, the yard tells the
operator each month, with the count of grants and the newest, through
their notifications: a setting and a timer the yard keeps for itself.
I would take A: the overview is one command, and a reminder nobody set is
one more line approved on reflex.

## 6. The beads it would become

In the order they would land.

**W1. The yard weighs each card (yardr).** Goal: every card is weighed
by the yard when it is written and again when it is approved, and the
weight is on the socket API. Where to look: `internal/store/runask.go`
(`runCommand`, `askedRun`, `approveAsk`), `runask_list.go`,
`runask_more.go`, `runask_pack.go`, `runask_source.go`, `runask_setup.go`,
`runask_mapping.go`, `runask_hook.go`, `runask_userkey.go`,
`secretask.go`, `asks.go` (`Ask`, `newAsk`, `ResolvedAsk.lines`),
`decisions.go` (`OpenAsk`, `DecidedAsk`, `BeadAsk`), `docs/api.md`.
Done when: `Ask.Weight` and `AskWeight` exist; each row of section 3.1's
table weighs as the table says, with a test per row and a test that a
narrowing form of each is routine; a writer's `Weight` is thrown away;
an approval whose fresh weight differs from the stored one is stale and
runs nothing, with a test for an ask written without weight;
`weight` is on `OpenAsk`, `DecidedAsk` and `BeadAsk` and documented in
`docs/api.md` with the five classes; the terminal card prints the
`WEIGHTY` lines; `sh .yardr/test` passes.

**W2. A weighty approval needs `weighed` (yardr).** Goal: the yard
refuses a weighty approval from a client that did not say its person saw
the weight, and the terminal asks for it. Depends on W1. Where to look:
`ApproveAsk`, `ApproveSetup`, `GiveSecret`, `AnswerAsk` in
`internal/store`, `internal/store/api.go` and `go generate
./internal/rpc`, `internal/cli/bead_ask.go` (`--approve`, `--secret`, a
mapping pick), `docs/api.md` (the argument, the refusal, the batching
rule). Done when: each call takes `weighed`; a weighty ask approved
without it is refused with the words of 3.3 and changes nothing; a routine
ask is approved without it as before; `yardr bead answer` at a terminal
asks for the ask's number on a weighty card, and without a terminal needs
`--weighed`; `docs/api.md` says a weighty card is never approved in a
batch; tests for each; `sh .yardr/test` passes.

**W3. yardr chat draws the weight (yardr).** Goal: in `yardr chat` a
weighty card stands out from routine ones. Depends on W1. Labelled `eyes`.
Where to look: `internal/chat/view.go` (`questions`), `secret.go`,
`style.go`, `docs/chat.md`. Done when: the `WEIGHTY` lines of a card are
drawn in the chat's `alarm` style and the rest of the card as before; a
test pins the drawing of a weighty and a routine card; `docs/chat.md`
says what the mark means; `sh .yardr/test` passes.

**W4. footplate draws the weight and gates the approval (footplate).**
Goal: a weighty card is drawn distinctly, ⌘↵ alone does not approve it,
and on macOS its approval passes Touch ID or the login password before
`weighed` is sent. Depends on W1 and W2. Labelled `eyes`: read
`yardr-design/readme.md` and take the colour from its tokens. Where to
look: footplate `src/api.rs` (`OpenAsk`, `BeadAsk`, `approve_ask`,
`approve_setup`, `give_secret`), `src/window.rs` (`approve`,
`send_approve`, `run_keys`, `approve_label`, `APPROVE_ELSEWHERE`),
`src/fake.rs`; LocalAuthentication through the objc2 bindings. Done when:
a weighty card shows its classes' lines in the head; its control reads
`Approve in <yard> with Touch ID`; the gate's success sends `weighed` and
its cancel sends nothing; on Linux, or where LocalAuthentication has no
policy available, the card shows the decline path of 3.3; tests against
the fake yard for each; a shot of a weighty card beside a routine one.

**W5. `yardr source grants` (yardr).** Goal: one command lists every
grant with when, by whom and on which card, and footplate can read the
same. Independent of W1 to W4. Where to look:
`internal/store/sourcecalls.go` (`SourceGrant`, `SourceGrants`, the
grant write and `source_granted` event), `internal/cli/sourcecmd.go`,
`internal/rpc/api.go` (`Calls`), `docs/api.md`, `docs/integrations.md`.
Done when: `SourceGrant.On` is written by a grant approved on a card;
`yardr source grants` prints the table and summary of 3.4 with its
narrowing flags and `--json`; `SourceGrants` is in `Calls`, documented,
and served on the listener to the operator's devices only; the docs show
the review timer; tests for the command and the listener rule;
`sh .yardr/test` passes.

**W6. footplate's grants view (footplate).** Goal: footplate shows every
grant as `yardr source grants` does. Depends on W5. Labelled `eyes`.
Where to look: footplate `src/api.rs`, `src/window.rs` (its modes),
`yardr-design`. Done when: the view lists the grants by source with the
weighty words for update and `--as` rows and each row's revoke line to
copy; it is read through `SourceGrants`; a yard without the call shows
that the yard is too old for the view; tests against the fake yard; a
shot.
