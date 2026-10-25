# Stage input and output: fields in, files out

Design yardr-ppgb, proposed 2026-10-25. A proposal as it was decided on, not a
description of what the code does today; the decision is on the bead.

## 1. The question

On 25 Oct mckean asked for input and output for stages that run scripts:

> provide input output for stages that run scripts … one stage is script with
> filter or so as input, output is data that can be used on the next stage,
> another script or agent or human.

A *process bead* is a bead whose type has its own flow (`docs/flows.md`): each
stage is worked by a script (an `exec` group), an agent (a `herdr` group) or a
person (a `manual` group), and the flow's outcomes move it on. Today the three
kinds hand data along badly. A script gets only the fixed `YARDR_*` environment
(`docs/groups.md`, the `exec` row: `YARDR_BEAD`, `YARDR_BIN`, `YARDR_HOME`,
`YARDR_STAGE`, …) and chooses the next stage with
`$YARDR_BIN bead done $YARDR_BEAD --outcome <name>` (`docs/flows.md`,
"Outcomes"). Anything richer travels in a note (`yardr bead note <id> --note -`),
which an agent reads in its brief (`internal/runner/runner.go`, `Brief`) and a
person reads on the bead's page. A note is prose with files hung off it; it is
not a named input a script can read without parsing a person's sentences, and
a file a stage produces has no home unless someone remembers to attach it to a
note by hand.

The example the design is shaped against is a `crm-report` bead type:

```text
fetch (exec, takes a filter) → adjust (agent) → approve (manual)
```

`fetch` runs a script with a filter (say `region=eu`), writes `report.csv`;
`adjust`, an agent, reads that file and writes `report-adjusted.csv` with a
commentary; `approve`, a person, opens both, approves or sends the bead back.
True when done: the script reads its filter without parsing a note, each stage
finds the files the stages before it left in a named place, and the person
sees the same files on the bead's card — with no new flow syntax, because
outcomes already branch.

The bead proposes a shape, to be tested rather than taken as given:

1. Input as named fields on the bead (`create`/`update --field k=v`), an exec
   session's environment as `YARDR_FIELD_<K>`, and the fields listed in an
   agent's brief and on footplate's bead card.
2. Output as files: a session's `$YARDR_OUT` (a fresh directory) kept on the
   bead as attachments at session end; the next stage gets them in `$YARDR_IN`
   (exec: a directory; agent: paths in the brief; person: listed on the card);
   an agent stage adds its own the same way.
3. No new flow syntax: outcomes branch.

Principle (mem-f0fk: generalisation, composability): one mechanism over special
cases. That cuts both ways, and section 2 holds the proposal against it: the
yard already has a file mechanism (blobs, hung off notes and mail), and a
second one that ignored it would fail the principle it cites.

Terms used below. A *field* is a named string on a bead (`region=eu`). A *stage
output* is a file a session left in its out-directory, kept on the bead with
the stage and session that produced it. A *blob* is the yard's existing
content-addressed file store (section 2). *Footplate* is the desktop and
Android client that reads the yard through the socket API (`docs/api.md`); the
browser UI in `internal/web` is its server-rendered sibling, and both draw the
same bead page shapes.

## 2. What exists today

**The bead.** `store.Bead` (`internal/store/beads.go`) holds `Labels []string`
and `Metadata map[string]any`. There is no `--field` on `yardr bead create` or
`yardr bead update` today (checked against the built CLI: create takes
`--label`, `--meta`, `--type`, `--stage`, …; update takes `--add-label`,
`--meta`, …), and no field of any kind is in a session's environment beyond
the fixed set. The two neighbours a field must not be confused with:

- *Labels route.* `checkLabels` (`internal/store/beads.go`) refuses a label
  with white space because routes match labels exactly; a label is a routing
  fact anyone may add with `--add-label`, and the route report already keys
  off labels such as `difficulty:3`.
- *Metadata is a free merged map.* `BeadPatch.Metadata` is merged by any writer
  (`--meta key=value`); the originator design
  (`docs/design/2026-10-14-yardr-2k0x-originator-subscription.md`) refused to
  put the originator there for exactly that reason, and existing keys
  (`model`, `pr_url`) are the yard's own bookkeeping, not a stage's data.

**Files today are blobs, and they already work.** `internal/store/blobs.go`:
a blob is any bytes under a cap, kept in the yard's home under `blobs/`
(`BlobsDir`) in a file named by the SHA-256 of its bytes, so the store file
does not grow by the bytes and the same bytes sent twice are one file.
`PutBlob` on the socket API (`internal/store/api.go`) puts one; a note carries
files as `[]BlobRef` (hash, type read from the bytes, byte count, sender's
name) through `yardr bead note <id> --file <path>` (`internal/cli/bead_note.go`),
at most `MaxNoteFiles` (64) per note, each at most `blob_max_bytes` (default
8 MiB, `DefaultBlobMax`). A blob nothing names any more goes with `Prune`,
after `BlobGrace` (one hour) so a paste put before the line it goes with is
not swept. The web serves a note's blob at `/blobs/<hash>/<name>`
(`internal/web/beads.go`, `handleBlob`, `serveBlob`: an image shows, a page
runs on the sandbox origin, everything else downloads), and the bead page
(`/beads/<id>`, `BeadPage`) draws each note's files below its words, a missing
one as a line saying so.

Checked on this machine on 25 Oct 2026: `~/.yardr/yard.db` is 161 MB and
`~/.yardr/blobs/` is 96 MB beside it. The bead's "~169 MB" is the right size
for the store file; the conclusion it supports is stronger than stated — file
bytes already live on disk under the yard home, content-addressed, pruned,
and served, and `yard.db` never held them.

**How a session starts and ends.** The dispatcher claims a bead into a session
(`store.Session`, `internal/store/routing.go`), a runner starts it, and
`Dispatcher.teardown` (`internal/dispatch/dispatch.go`) stops it and calls
`MarkStopped`; the session-token design
(`docs/design/2026-10-21-yardr-qbqq-session-tokens.md`, §3.1) already hangs a
post-stop job off that moment (its `count_due` and `Uncounted`), so a sweep
that must happen once, after the process has exited, has a place to live. The
three runners differ in what a session is:

- *Exec* (`internal/runner/exec.go`): `sh -c <cmd>` in the depot, environment
  built by `Env(job, group)` (`internal/runner/runner.go`) plus the exec-only
  set (`YARDR_DEPOT`, `YARDR_BASE`, `YARDR_BRANCH`, `YARDR_WORKTREE`,
  `YARDR_TEST`, `YARDR_TYPE`, `YARDR_TRAIN`, `YARDR_KIND`, `YARDR_MODEL`,
  `YARDR_BIN`, `YARDR_HOME`). The script advances its own bead; exiting
  without doing so ends the session as died. Its stdout and stderr are
  appended to a session log.
- *Herdr* (`internal/runner/herdr.go`): an agent in a worktree, prompted with
  a brief written into the worktree's git dir by `Brief`
  (`internal/runner/runner.go`): the group's prompt, the depot's rules, the
  bead's title and body, how to report back, the flow map and the stage file.
  The bead's notes are *not* in the brief; the agent reads them with
  `yardr prime --bead <id>`.
- *Manual*: nothing is started. A person claims the bead
  (`yardr bead list --group <name>`), reads its page, and reports an outcome.

**A linked yard.** The `peer` runner (`internal/runner/peer.go`) queues the
bead for a linked yard with `PeerBeadSend`; the other yard works its own bead
through a flow of its own and returns an outcome. What crosses is
`PeerBeadMessage` (`internal/store/peerbeads.go`): title, body, type,
priority, outcomes, notes (bounded: `MaxPeerNoteBytes` 16 KiB each,
`MaxPeerNotes` 128 KiB together), links, and optionally what the work starts
from (`Git`). Files cross a link today only with mail, through the same blob
store: a file a peer sent is taken with `takeFile` under `peer_file_max_bytes`
(default `blob_max_bytes`, never more than 32 MiB), and a `BlobRef` the yard
did not take carries `NotTaken` and why. The hosted Station is not a separate
codebase: a hosted yard is a yard the service runs (`docs/edge.md`), reached
by footplate through the edge link, running this same store and these same
runners.

**The screenshot on `eyes` beads.** A builder working a bead labelled `eyes`
attaches a screenshot to its note with `--file` (the builder's brief: "put a
picture on the note with --file"). It is a blob like any other note file; the
only special thing about it is the convention that there is one.

## 3. The proposal

The proposed shape survives testing with two changes, both forced by what
already exists:

- **Output files are blobs, named by the bead as stage outputs — not a new
  attachment store.** The bead's guess said "kept on the bead as attachments";
  the yard's attachments *are* blobs (section 2), so a stage output is a
  `BlobRef` plus provenance, and `YARDR_OUT` is only the directory a session
  writes into before the sweep turns its files into blobs. One storage
  mechanism, as mem-f0fk asks.
- **Fields are their own map, not metadata and not labels.** A stage's filter
  is data with a reader (the next stage), a writer (whoever set it) and a
  lifetime (the bead's); metadata is the yard's bookkeeping and labels are
  routing. A third, small, string-only map keeps each mechanism honest.

### 3.1 Fields: named strings on the bead

A bead gains `Fields map[string]string` (`internal/store/beads.go`), stored on
the bead's row beside `Labels` and `Metadata`, and carried in `--json`
everywhere the bead is printed. Strings only: a field's readers are an
environment variable and a line in a brief, and both are strings; a stage that
needs a number parses it, as a script parses any environment. No schema, no
types (left out by the bead, and right to leave out: the `crm-report` filter
is `region=eu`, not a record).

- **Setting them.** `yardr bead create "<title>" --field region=eu` and
  `yardr bead update <id> --field region=eu`, each repeatable; `--field
  region=` clears that field, as an empty environment value would read.
  Over the socket these are `NewBead.Fields` and `BeadPatch.Fields` (a merged
  map, as `BeadPatch.Metadata` is merged), so a script sets a field for the
  next stage with the `yardr` it already has (`$YARDR_BIN bead update
  $YARDR_BEAD --field rows=412`) and an agent does the same from its pane.
  A process's first stage gets its fields from whoever creates the bead — a
  person, a hook, or an earlier bead's stage.
- **Who may write them.** A field is the input of the next stage's script, so
  a write is gated, not merged by any caller as `--meta` is today. Setting a
  field, and recording an output by hand (3.5), asks a new `Action` in
  `internal/store/policy.go`, "set a bead's stage data", through `may`. Its
  core rule: a call from a session's pane (`paneSession`, as `UpdateBead`
  already uses to refuse a pane pinning a flow) may write only that session's
  own bead; any other caller may write wherever it may update the bead
  today. Without the gate, an agent working one bead could set the filter,
  or plant the input file, that another bead's script stage reads.
- **Names.** A field name is `[a-z][a-z0-9_]*`, at most 64 bytes: it must
  become the tail of `YARDR_FIELD_<NAME>` (upper-cased) without escaping, and
  a name that cannot (`Region`, `my-filter`) is refused at the write with the
  rule, never silently mangled into a different variable. This is
  `checkLabels`' lesson applied before the fact: an input that can never be
  read back fails loudly at the write, not silently at the read.
- **Size.** At most 32 fields on a bead, each value at most 4 KiB, the whole
  map at most 32 KiB. A filter, a date range, an account name fit easily; a
  CSV does not, and is a file (3.2). The bounds are named constants beside
  `MaxPeerBeadBody` (64 KiB) and refused with their numbers, as note files
  are refused with theirs.
- **Reading them, per kind.**
  - *Exec:* every field is one variable, `YARDR_FIELD_REGION=eu`, set by the
    same `Env` that sets `YARDR_BEAD` today, whether or not the bead has any
    (a stale variable inherited from the daemon is withheld, as `YARDR_TEST`
    and `YARDR_BASE` already are in `internal/runner/exec.go`'s `withheld`).
  - *Agent:* the brief gains a `## Fields` section between the bead's body
    and the reporting-back section (`Brief` in `internal/runner/runner.go`),
    one line per field, with each value encoded as a JSON string literal so a
    newline cannot escape the data block, and the session's pane environment
    carries the same `YARDR_FIELD_*` variables, so a script the agent runs
    reads what the brief printed. Peer-origin fields stay under the peer
    marking and are data, not instructions. Absent when the bead has no
    fields, as the `## Sources` section is absent without calls.
  - *Person:* the fields are a table on the bead's card (3.5), and
    `yardr bead show` prints them under the labels, one `name value` per
    line, with the same in `--json`. A person edits them on the card or with
    `bead update --field`, which is how the `approve` stage's answer (a
    corrected filter for a re-run) is written.
- **What fields are not.** Not secrets: a field is printed in a brief, on a
  card and in `bead show --json`, so a credential in one is published to
  everyone who may read the bead. Secrets keep their own path
  (`GiveSecret`, source grants; `docs/design/2026-10-12-yardr-0qs5-a-secret-from-a-person.md`).
  Field values are untrusted data: the brief quotes them, the card escapes
  them, and no yardr code interpolates them into a command, prompt instruction
  or flow. An exec group's own command treats its environment as input and
  validates it for its purpose. Not routing: no route matches a field, and a
  flow names none. If a value must decide who works a stage, it is a label;
  section 3.4 keeps that line.

A worked `fetch` stage, end to end:

```sh
# group crm-fetch (exec), cmd: crm-fetch --region "$YARDR_FIELD_REGION" --out "$YARDR_OUT"
# bead created with: yardr bead create "CRM report" --type crm-report --field region=eu
crm-fetch --region eu --out /home/…/stageio/crm-1/<session>/out   # writes report.csv there
$YARDR_BIN bead done $YARDR_BEAD                                  # outcome done → adjust
```

### 3.2 Files out, files in: one sweep at session end

Each session that can produce files gets two directories under the yard home,
made by the runner at start and named in its environment:

```text
<home>/stageio/<bead>/<session>/in/    what the earlier stages left (3.3)
<home>/stageio/<bead>/<session>/out/   empty at start; what this stage leaves
```

Beside `blobs/` and `worktrees/` in the home layout (`internal/config`), so an
isolated yard carries its stage files with it and removing the home removes
them. The variables are `YARDR_IN` and `YARDR_OUT` for exec and herdr sessions
alike (an agent's pane has them, and its brief names them); a manual session
starts nothing, so a person gets the same two sets rendered on the card
instead (3.5). The yard creates the session directories as `0700`; the bead
and session path components are generated ids, never values taken from a
field, file name or peer message.

**The sweep.** When a session ends — in `teardown`, beside the `MarkStopped`
the token count already hangs off — the dispatcher sweeps that session's
`out/`: every regular file in it is put with `PutBlob` and recorded on the
bead in one store call, `RecordStageOutput(ctx, bead, session, stage, files)`,
which appends one entry to a new list on the bead's row:

```json
{"stage": "fetch", "session": "…", "at": "2026-10-25T…Z",
 "files": [{"hash": "…", "name": "report.csv", "bytes": 18211, "type": "text/csv"}]}
```

The `files` of an entry are `BlobRef`s as a note carries them; the entry adds
only the stage, the session and the time. The directories are then removed:
the blobs are the record, and a kept directory would be a second copy under a
name nothing prunes. A session that leaves `out/` empty records nothing. A
file over `blob_max_bytes` is not put: the entry records it by name with the
refusal (as a peer file the yard did not take carries `NotTaken`), the sweep
still keeps the rest, and the bead's page and the next brief say the file was
left behind and why — a stage that produced one oversize file must not lose
the small ones beside it, nor fail silently. An exec script, an agent and a
person (through the card, 3.5) all produce outputs this one way; the agent's
path is identical to the script's, which is the bead's proposal (2) kept: it
writes into `$YARDR_OUT` with its tools, and the sweep at its session's end
does the rest.

`out/` is flat: only regular files directly inside it are outputs, and each
name must be non-empty and pass `store.CheckBlobName` as one base name.
Directories, symlinks, special files, invalid names and duplicate names are
reported as refused entries, with names safely escaped for display; the sweep
does not follow or recurse into them or use their names in another filesystem
path. This keeps a stage from making the daemon read a path outside its output
directory.

The checks hold against a session that changes the directory while the sweep
reads it: an exec script's children can outlive it, and an agent's pane runs
as the yard's own user, so either may rename `out/` itself to a symlink, or
swap a checked file for one, between the check and the read. The sweep
therefore opens the session directory as an `os.Root` (Go's handle that never
leaves the directory it was opened on, symlinks included), takes `out/`
through that root only if it is a directory and not a link, and opens each
entry through it with `O_NOFOLLOW|O_NONBLOCK`, deciding "regular file" and
the size from `Stat` on the opened file, never from the listing. It reads at
most `blob_max_bytes` + 1 bytes, so a file that grows after the check is
refused as oversize rather than read without end, and a FIFO left in `out/`
cannot hang the teardown. `PutBlob` stores accepted bytes as the existing
`0600` blob files.

**Files in.** At a session's start the runner fills its `in/` from the bead's
recorded outputs (3.3) by copying each blob's bytes to
`in/<encoded-stage>/<name>`. These are copies, never hard links: a session
can write its input file, and a hard link would let it change the content-
addressed blob for every bead that names those bytes. The copies and their
parent directories are `0600` and `0700` respectively. The stage name is
encoded as one path-safe component before it is joined to `in/`; flow stage
names may contain `/` and `..`, so the raw name is never a filesystem path.
Each file name, whether this yard swept it or a linked yard sent it (3.6), is
checked again with `store.CheckBlobName`, and refused if empty, before it is
joined to that directory: the record is the input here, and a name that came
from another yard was never checked by this one. Two names in one stage that
differ only in case are one file on a case-insensitive file system (the
default on macOS), so the second is listed as left out, not copied over the
first.
The next stage reads plain paths, never hashes. An agent's brief maps each
source stage and file name to its path under `YARDR_IN`, so the agent need
not list the directory to know what it was handed; a person sees the same
list on the card, each opening as a note's file opens today. File contents
from another yard remain data, not instructions to the agent.

**Limits.** Per file, `blob_max_bytes` (8 MiB default) — the yard's existing
answer to "how large a file may the yard hold", refused at `PutBlob` already.
Per stage, at most 64 files (`MaxNoteFiles`' number, for the same reason: one
letter's worth), and at most 256 files over a bead's life, so a flow that
loops a stage cannot grow a bead's outputs without bound; the sweep refuses
the excess by name as it refuses an oversize file. A stage that genuinely
produces thousands of small files produces one archive among them — that is
the script's choice to make, visible in its output list, not the yard's to
guess at.

**Retention.** A stage output is named by the bead's record, so `Prune` keeps
its blob exactly as it keeps a note's, for exactly as long as the bead names
it. When a bead is closed, its outputs are kept for `YARDR_EVENT_RETENTION`
(90 days by default, as sessions and events are) after `ClosedAt`, and then
the bead's output entries are dropped by the same `Prune` that sweeps unnamed
blobs, which then collects the bytes on its next pass; an open bead's outputs
are never pruned. The `stageio/` directories themselves live only for their
session. A bead reopened within the window finds its outputs; after it, the
page lists them as missing, as the bead page already lists a note's missing
file — a line saying so, never a broken link.

### 3.3 A stage sees all earlier outputs, not only the last stage's

`in/` holds every earlier stage's outputs, each under its encoded stage name
(`in/<encoded-fetch>/report.csv`, `in/<encoded-adjust>/report-adjusted.csv`),
oldest stage first. The brief shows the original stage names beside those
paths.
In the example, `approve` must open `fetch`'s original beside `adjust`'s
rewrite; a last-stage-only rule would force `adjust` to copy forward
everything a later stage might need, which is data copying as a workaround
for a naming choice. Namespacing by stage makes keeping everything safe:
two stages may each leave a `report.csv` and neither overwrites the other,
and a stage's own earlier round (a bead sent back and worked again) is
`in/<encoded-fetch>/` holding the newest round's files, with the bead's page
listing each round's entry separately — the record keeps all rounds, and the
directory hands the stage the latest of each stage, since a re-run wants the
current inputs, not a history lesson. The page and the brief's input list name the
stage each file came from, so provenance is never a guess from a file name.

### 3.4 Fields carry data; labels route — keep it that way

Yes, kept, and the design relies on it. A label is matched by routes, shown
in listings as a routing fact, and cheap to add by anyone; a field is read by
the next stage, may hold a value no route should ever see (`region=eu-west-1`
is nobody's routing decision), and changes without moving the bead anywhere.
The failure each prevents: routing on fields would let a stage redirect its
own bead by writing its filter (a script setting `region` must never change
who works `adjust`), and carrying data in labels would put a filter in every
listing, route report and label match the yard runs. The boundary is enforced
by absence, not by a rule to police: routes read labels (`internal/store` route
matching), briefs and scripts read fields, and nothing reads the other. A
value that starts as a field and later must route is promoted by a person to
a label with `bead update --add-label`; the field stays as the record of what
the stage actually read.

### 3.5 The card: footplate, desktop and Android

One shape serves all three readers, as the bead page's shapes already do
(`docs/api.md`: the yard computes the view once so the readers draw without
computing). `store.Bead` gains `fields` (the map) and `outputs` (the entries
of 3.2) in JSON; `internal/web`'s `BeadPage` (`internal/web/beads.go`) carries
both, and the socket's bead read carries them beside the notes.

- **The bead page and footplate's bead card** show a `Fields` table (name,
  value; editable by whoever may update the bead) above the notes, and an
  `Output` list grouped by stage — name, size, stage and time — under it.
  Each file opens exactly as a note's file opens today: an image inline, a
  page on the sandbox origin, the rest as a download, all through
  `/blobs/<hash>/<name>` (`handleBlob`), with a missing blob drawn as a line
  saying so. No new serving route, no new permission: whoever may read the
  bead reads its outputs, as they read its notes.
- **A manual stage's input and output.** The person at `approve` reads the
  input files from the card and leaves their own — an annotated PDF, a
  signed-off spreadsheet — with an upload on the card (a file input on the
  web page; footplate's own picker on desktop and Android) or, in a terminal,
  `yardr bead note <id> --file <path>` today and the same sweep's CLI form,
  `yardr bead output <id> --file <path>`, which records the file as the
  current stage's output without a session to sweep. Both are refused unless
  the bead is in a manual stage, and both ask the stage-data gate of 3.1, so
  an agent's pane cannot add an output to another bead's record. Approving is still the
  outcome (`bead done --outcome done`); the file is evidence beside it, not
  a second way to move the bead.
- **Android** gets no second design: footplate's Android client reads the
  same bead JSON and opens the same `/blobs/` URLs through the edge link, as
  it opens a note's file today. A large output downloads; nothing in the
  design streams or renders a file on the device beyond what the blob's type
  already does in a browser.

### 3.6 Across a linked yard, and the hosted Station

**Fields cross in the bead message; files cross as file messages do.**
`PeerBeadMessage` gains `Fields map[string]string` and
`Outputs []PeerStageOutput`, where each output keeps its source stage and
timestamp beside its `BlobRef`s. `PeerReturnMessage` gains the same members.
The latest output round for each source stage travels, matching the inputs a
runner gives a later stage (3.3). The new type is named `PeerStageOutput`:
`PeerFile` is already the string constant for the peer message kind, not a Go
file-reference type. Fields and output references count against
`MaxPeerBody`; notes are fitted to the space that remains, and outputs that
cannot fit are reported in an `outputs_left_out` count rather than silently
losing their stage or file names.

The file bytes remain separate `PeerFile` messages, sent before the bead
message that names them, and require the link to carry `file` and the other
yard to advertise that it understands `PeerFile`. Each transfer uses the
existing `takeFile` path and `peer_file_max_bytes`. The receiving yard stores
accepted bytes in its own blob store and keeps each output under its original
source stage. A received field is checked by the same name and size rules as a
local write (3.1), and a received file name by `CheckBlobName`; one that fails
is recorded as not taken, never stored under a name this yard would refuse. A referenced hash it does not hold is recorded with `NotTaken`
and a reason; the bead still arrives. If the link cannot carry files, the
sender marks the outputs as not sent in the bead message and sends the bead
without file messages. This gives the next stage and the card an explicit
record of what did not cross, rather than dangling hashes.

The peer marking (`BeadPeer.Marking()`): another yard's text is data, not
instructions, covers displayed field values and file names. Agent briefs also
mark peer-provided file contents as untrusted data; the yard does not execute
or interpolate them. A flow that accepts beads from a linked yard describes
how its stages validate those values, as it already says what its scripts do.

Two limits keep this honest. A bead that crosses twice (sent on, then back)
carries its outputs both ways only while they fit the peer caps; the record
on each yard names what the other did not take. A peer running an older yardr
ignores the new JSON members (`PeerBeadVersion` stays the same), but it may
not advertise `PeerFile`; in that case the sender leaves output refs marked
not sent and still sends the bead, so no stage receives a dangling path.

**The hosted Station needs no separate mechanism.** A hosted yard is this
code on the service's machine (`docs/edge.md`): fields live on its beads,
blobs under its home, the sweep in its dispatcher, and footplate reaches all
of it through the edge link by the same `/blobs/` route a connected yard
serves. The one Station-specific fact is operational, not structural: a
hosted yard's `blob_max_bytes` and disk are the service's to set, so the
caps of 3.2 are settings there as they are on any yard, and a process bead
whose stages produce files near the cap belongs on a yard whose home has
room for them. The design names no Station-only field, route or store.

### 3.7 What this does to notes, attachments and the `eyes` screenshot

- **"Attachments into blobs" is already done, and this design uses it.** The
  unfiled idea — that a bead's attachments should be blobs rather than bytes
  in `yard.db` — is what `internal/store/blobs.go` already is: notes and mail
  carry `BlobRef`s, bytes live under `blobs/`, `yard.db` holds hashes. Stage
  outputs are the same `BlobRef` under a second naming (by stage, beside by
  note). There is no second file store to design, and nothing to subsume:
  the mechanism the idea asked for is the one this design builds on.
- **The `eyes` screenshot stays on its note; it may also be an output.** The
  brief's rule ("put a picture on the note with --file") is how a person
  later finds the shot beside the words that explain it, and that stays. A
  builder on an `eyes` bead that also writes the shot into `$YARDR_OUT`
  leaves it as the stage's output, where the next stage (review) gets it in
  `$YARDR_IN` without reading a note — one mechanism (blobs) under both
  namings, each used where it reads best. The design does not retire the
  note attachment, because a note's file answers "what did you see?" and a
  stage output answers "what did the stage produce?"; collapsing the
  questions would lose the words the shot travels with.

## 4. Left out

- **Implementation.** Beads follow the design (section 6); this doc writes no
  code and changes no flow.
- **A flow DSL for data mapping.** No stage file declares "pass `fetch`'s
  `report.csv` to `adjust` as `input.csv`", no expression renames or picks
  fields, and no flow syntax is added at all (the bead's proposal 3, kept).
  A stage finds its inputs by stage name in `$YARDR_IN`; a flow whose stages
  need a different arrangement names its stages accordingly. Outcomes branch;
  data accumulates.
- **Typed schemas.** Fields are strings and files are bytes with names. A
  depot that needs `region` to be one of three values checks it in the
  script that reads it, where the refusal names the bead's field and value.
- **Reading a stage's output while it still runs.** The sweep is at session
  end; a stage that must hand data to a concurrent stage uses a note or a
  source, as today. Nothing tails `out/`.
- **Fields as route keys, and outputs as bead text.** Neither is indexed,
  searched or matched (3.4); `yardr bead list` gains no `--field` filter in
  this design. Named here as a later bead if a depot asks for it.

## 5. Open questions

Each is put to mckean with a recommendation. The links identify which
implementation bead takes the answer; these asks stay open until answered.

1. **The per-bead output cap (256 files) and the closed-bead retention (90
   days after close).** (a) As proposed: generous for a report flow, bounded
   against a looping stage, and the retention the yard already keeps
   sessions for. (b) No per-bead cap, only the per-file and per-stage caps:
   simpler, and a bead in a tight loop grows until the disk does. (c) A
   shorter retention (30 days): hosted yards hold less, and a person
   reopening a month-old report finds its files listed as missing. I would
   take (a); the numbers can become settings if a depot outgrows them. The
   answer is taken by [bead 2](#2-the-sweep-a-sessions-out-becomes-the-beads-outputs).
2. **May a person leave a manual stage's output from the terminal only, or
   must the cards take uploads in the first build?** (a) Card and terminal
   together ([beads 6–8](#6-the-yardr-card-shows-fields-and-outputs-and-takes-a-persons-files)):
   the `approve` person is the design's centre, and
   a terminal-only upload leaves them attaching the file to a note instead
   — the habit this design replaces. (b) Terminal first (`bead output
   --file`), card upload as a follow-on: smaller first landing, and the
   card still shows and opens every file from the start. I would take (a);
   the upload is one `PutBlob` the card's note-file path already proves.
3. **Does an agent's `YARDR_IN` belong in its worktree or beside it?** As
   proposed it is a directory under the yard home named in the brief, outside
   the worktree, so input files never appear as uncommitted changes in a
   bead's diff. (a) As proposed. (b) Inside the worktree (say `.yardr-in/`,
   ignored as briefs' directory is): one fewer place to look, and a stage
   can commit an input by accident the first time it runs `git add -A`. I
   would take (a), for the same reason briefs live outside the tracked tree.
   The answer is taken by [bead 3](#3-fields-and-input-reach-a-session-exec-and-herdr).
4. **Should `RecordStageOutput` also write a note?** A stage that leaves
   files could get a system note ("fetch left report.csv") so the bead's
   history reads without the output list. (a) No: the bead page, the brief
   and `bead show` list outputs by stage, and a note per stage per round is
   noise in the notes a person reads for words. (b) Yes, one line per
   session that left files. I would take (a); the output entries carry the
   stage and time a note would repeat. The answer is taken by
   [bead 2](#2-the-sweep-a-sessions-out-becomes-the-beads-outputs).

## 6. The beads it would become

In dependency order. The yardr store, runner, CLI and web work is in the
`yardr` depot; the desktop and Android clients are separate depots and follow
the socket and web API bead. These are listed here and filed once the open
questions above are decided. Section 5 links each ask to the bead that takes
its answer.

### 1. Fields on a bead, set and shown

Goal: `Bead.Fields map[string]string` with the name and size rules of 3.1,
`NewBead.Fields` and a merged `BeadPatch.Fields` (an empty value clears),
`--field` on `yardr bead create` and `yardr bead update` under the
stage-data gate of 3.1, the fields printed by
`yardr bead show` under the labels and carried in `--json` and in the socket's
bead reads. No migration beyond the bead row's JSON, as `Metadata` needed
none. Where to look: `internal/store/beads.go` (`Bead`, `checkLabels` for the
refusal's shape), the bead create and update commands in `internal/cli`. Done
when: a store test creates a bead with two fields, updates one and clears one;
a call from one session's pane is refused setting a field on another bead;
a name with a capital or a hyphen and a 33rd field are refused with the rule;
`bead show` prints the fields and `--json` carries them; a bead with no fields
prints exactly as before.

### 2. The sweep: a session's `out/` becomes the bead's outputs

Goal: the store's output record and `RecordStageOutput` (3.2, one transaction,
daemon-only as `SetHandle` is), with the per-file, per-stage and per-bead caps
refused by name; `Dispatcher.teardown` sweeps `out/` through `PutBlob` before
`MarkStopped`, using the path and file rules in 3.2, and removes the session's
`stageio/` directories; `Prune` drops a closed bead's entries after
`YARDR_EVENT_RETENTION` and so frees their blobs. Where to look:
`internal/store/blobs.go` (`PutBlob`, `Prune`, `MaxNoteFiles`),
`internal/dispatch/dispatch.go` (`teardown`). Depends on 1 so the record ships
with the bead shape. Done when: a dispatch test with a stand-in runner writes
two files to `YARDR_OUT` and gets one output entry naming both blobs, the
directory is gone, and empty `out/` records nothing; a symlink, nested
directory, FIFO, invalid name and duplicate name are refused without reading
their targets or hanging, and an `out/` replaced by a symlink to another
directory records nothing from it; a file over `blob_max_bytes` is recorded as not taken with the rest
kept; a prune test drops a bead closed past the retention and keeps an open
bead's.

### 3. Fields and input reach a session (exec and herdr)

Goal: `Env` (`internal/runner/runner.go`) sets `YARDR_FIELD_<NAME>` for every
field and withholds a stale one as `YARDR_TEST` is withheld; every session
gets `YARDR_IN` and `YARDR_OUT` under
`<home>/stageio/<bead>/<session>/`, `in/` filled from the bead's recorded
outputs by stage (3.3, latest round per stage); `Brief` gains the `## Fields`
and `## Input files` sections, absent when empty, with peer-origin values and
files marked as untrusted data. Depends on 1 and 2. Where to look:
`internal/runner/runner.go` (`Env`, `Brief`), `internal/runner/exec.go`
(`withheld`), `internal/config` for the home layout. Done when: an exec test's
script prints its `YARDR_FIELD_REGION` and finds
`in/<encoded-fetch>/report.csv` from a seeded output record; a brief test
shows both sections for a bead with a field and an input, with a multiline
value kept inside its quoted data representation, and neither section for a
bead with neither; a dir depot's bead gets the variables with no worktree
involved; changing an input copy leaves the blob unchanged; a recorded name
that `CheckBlobName` refuses, and a second name differing only in case, are
listed as left out and nothing is written outside `in/`.

### 4. `yardr bead show` and `prime` list a bead's outputs

Goal: the output entries printed by stage (name, size, stage, time; a
not-taken or missing file said so) in `bead show` and in the prime a session
reads, with the same under `outputs` in `--json`. Depends on 2. Done when: a
CLI test shows a bead with two stages' outputs grouped by stage, and no output
section for a bead with none.

### 5. Fields and outputs cross a linked yard

Goal: `Fields` and `Outputs []PeerStageOutput` on `PeerBeadMessage` and
`PeerReturnMessage` (3.6), each output preserving its source stage and refs;
file bytes sent through the existing `PeerFile` message kind and `takeFile`,
subject to the link's `file` permission and `peer_file_max_bytes`; received
files are marked `NotTaken` when absent and kept under their source stage.
Peer-origin fields and file contents stay marked as untrusted data in briefs.
Where to look: `internal/store/peerbeads.go`, `internal/store/peerfiles.go`
(`takeFile`, `peer_file_max_bytes`). Depends on 2 and 3. Done when: a two-yard
store test sends a bead with a field and outputs from two stages and finds
their provenance on the received bead, a peer without file support gets no
dangling refs, an oversize file is recorded not taken with the bead still
taken, a field name or file name this yard would refuse is recorded not taken, and a return carries the worked bead's new output back.

### 6. The yardr card shows fields and outputs and takes a person's files

Goal: `BeadPage` carries `fields` and `outputs` (3.5); the yardr bead page
shows the fields table and stage-grouped output list, each file opening
through `/blobs/<hash>/<name>` as a note's file does; a web upload and
`yardr bead output <id> --file <path>` record a person's file as the current
stage's output. The socket's bead read carries the same fields and outputs
for clients to render. Depends on 1, 2 and 4; takes open question 2's
decision. Done when: a web test shows fields and two stages' outputs with
working blob links, and an uploaded file appears as the manual stage's output;
`bead output` on a bead outside a manual stage, or from another bead's pane,
is refused.

### 7. Footplate desktop shows and edits stage data

Depot: `footplate`. Goal: the bead card shows and edits fields, lists outputs
by source stage, opens their blob links, and lets a person upload a manual
stage output when open question 2 chooses card upload in the first build.
Depends on 6's socket/API shape. Done when: a client test covers editing a
field, opening an output, and uploading an output through the yard's existing
blob API.

### 8. Footplate Android shows and edits stage data

Depot: `footplate-android`. Goal: the bead card shows fields and outputs,
opens output links through the edge connection, and provides a file picker
for manual stage output when open question 2 chooses card upload in the first
build. Depends on 6's socket/API shape. Done when: a client test covers
displaying both data types, opening an output, and uploading one through the
edge connection.

Named and not designed: a `--field` filter on `yardr bead list`; routes
that match fields; a hosted-yard disk quota for outputs beyond the existing
`blob_max_bytes` setting; carrying a stage's outputs into a depot (a stage
that wants its file in the repo commits it, as any session does).
