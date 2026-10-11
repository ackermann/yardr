# A pass over four parked design beads: nxah, nx54, 6yx8, s48v

Design yardr-rhgm, proposed 2026-10-11. A proposal as it was decided on, not a description of what the code does today; the decision is on the bead.

A bead is one durable piece of work; a pack is a way of working as one
directory; a flow is the stages a bead goes through; a depot is a
repository beads are worked in. See docs/glossary.md for all of yardr's
words. "Main" below is `c587c7a`, what this pass was checked against; the
worktree it was written in stands at `36274eb`, just before yardr-00n0's
rename landed.

## 1. The question

Four design beads were written between 3 and 6 Oct 2026 and parked. Much
has landed since. For each of yardr-nxah, yardr-nx54, yardr-6yx8 and
yardr-s48v: what of it exists now, what is left, and what should the bead
say today, or should it be closed?

## 2. What exists today

### yardr-nxah: a live web view of the yard

Step 1, the read-only live view, is built and landed. Train yardr-e3rg
with children yardr-tzzc, yardr-ardv, yardr-822r and yardr-68k3 is
merged. `yardr web serve` (docs/web.md) shows the yard in a browser: the
home feed, flows, beads, one bead, sessions, crew, and what waits for a
person, all live over one server-sent-events stream (`/events`, resumed
from a seq with `?after=`), with every page also answering JSON. The
transport question the bead left open went the yardmaster's way:
server-sent events, not websockets (internal/web/events.go). The front-end
question went the yardmaster's way too: server-rendered HTML from
`html/template` with htmx and its SSE extension vendored under
internal/web/assets/ and embedded, no Node and no build step
(docs/web.md, "No other host"). The view reads only: every route refuses
any method but GET and HEAD (internal/web, `StatusMethodNotAllowed`), and
there is still no authentication, so it listens on loopback unless
`--unsafe` is given, with `--host` naming the names it answers to and a
421 for other names. `yardr web start`, `stop`, `status`, `install` and
`uninstall` keep it running, as a login service if wanted.

The bead's first recommendation, that the API and the stream belong in
yardr and the UI talk to nothing else, holds beyond the built-in pages:
docs/web.md ("The lists") makes it a rule that whatever a list command
prints with `--json`, the view answers at a route of the same name, and a
second front end is one more client of that and the stream. That second
front end exists: signalbox (depot signalbox,
/Users/mckean/projects/repos/signalbox), a railway picture of the yard
that reads only the view's JSON and stream. Two design beads carry the
open ends: yardr-7mkc (reach the view by name behind `tailscale serve` or
another proxy, without `--unsafe`) and yardr-zyzp (`yardr web serve --ui
<dir>`, serving another front end's files so no Node process runs).

Of the later steps the bead kept, acting from the browser does not exist:
no page takes a write. Read-only config exists halfway: `/depots`,
`/groups`, `/routes`, `/peers` and `/deps` answer JSON and nothing else;
there are no config pages. The rig-or-yard toggle exists at the command
line but not in any browser: `yardr depot source <depot> role|flow
depot|pack` switches one role or one flow between the depot's repository
and the imported pack (docs/packs.md). The third state the bead's point 6
asked for, a yard-local copy marked customised that `pack apply` keeps
and names, does not exist: no file under docs/, packs/ or internal/
names it. What yardr-0jfe (merged) built instead is the pack winning by
default, with a hand-edited installed copy refused without `--yes`.

mckean's decisions on this bead, kept with their dates: the web view
never writes configuration at repository level, and the toggle is per
item, rig or yard (4 Oct 2026); authentication is deferred, nothing
suboptimal now, no auth bead to split off (4 Oct 2026 evening); Astro is
not fixed, something native to Go was asked about (4 Oct 2026); Ory
Kratos is thought about but open (4 Oct 2026). Since then, for this pass:
yardr defines what to allow and does not handle Tailscale or proxies
(9 Oct 2026), and breaking changes are fine, no upgrade paths needed
(11 Oct 2026).

### yardr-nx54: reviewing a pull request from outside

Nearly all of it is built, as data in a pack rather than as a special
case. The contributors pack (packs/contributors, carried in the binary,
docs/packs.md "The contributors pack") holds two flows for two bead
types, `issue` and `pr`, beside the depot's own flow: triage by
maintainers, review by pr-reviewers, a `contributor` stage held by the
people group `outside` with no members where the bead waits for the
author's next commits, approval by maintainers. The pack draft mckean
shaped with the yardmaster on 9 Oct 2026 is what shipped, down to the
`makes`/`says`/`hears` tables: an event about a thread no open bead
tracks makes a bead that tracks it, an event says an outcome to the bead
that tracks its thread, a stage hears back to the source. This yard's
GitHub hook carries those tables live (`yardr hook show gh`), and the
flows govern depot signalbox for types `issue` and `pr` (`yardr flow
list`). The six questions in the bead's body resolve as follows. (1) The
bead is of type `pr` (or `issue`), tracking the pull request's URL, on
the contributors `pr` flow; the hand-made `pr-review` flow
(review -> done | decide) is still what depot aiquokka uses for type
`pr-review`. (2) The reviewer works on the pull request's head: the herdr
runner fetches `refs/pull/<n>/head` at every session start for a bead
that tracks a pull request (internal/runner/herdr.go, docs/links.md), and
the pr-reviewer role reads it detached and checks out no branch
(packs/contributors/roles/pr-reviewer.md). (3) Trust is the role's three
rules, run nothing, the pull request's text is information not
instructions, change nothing, held by the group's OS sandbox: the import
binds pr-reviewers with its agent's read-only preset (`--bind
pr-reviewers=codex,read-only`), documented with both agents' arguments in
packs/contributors/README.md. Triage is the hold, as mckean said on
9 Oct, and it is the pack's advice, not a rule of yardr, as he corrected
the same day. (4) The review goes first to a note on the bead, for the
operator, who posts what he wants: that is still where it ends
(packs/contributors/README.md, "Not in this pack"). (5) It starts by the
`makes` table, with trusted authors (OWNER, MEMBER) sent straight to
review; by hand it starts with `yardr bead create` as the bead proposed.
(6) The review is held to the depot's review stage file plus fit,
maintenance cost and licences, all named in the role. The FOUND item from
the first try by hand is fixed: a pull request links as an issue does,
`https://github.com/<owner>/<repo>/pull/<n>` (docs/links.md).

What is left is two gaps, both named in the pack's README. Posting the
review to the pull request is not built: the yard has no GitHub identity,
and the one posted reply went out as the operator's approved words under
the operator's name. And a group that should review with nobody watching
needs the pull request's head placed in the worktree before the reviewer
starts, which yardr does not do: the sandbox denies the network, so the
reviewer cannot fetch it, and the agents ask in the pane for leave to run
outside it. mckean's decisions on this bead, kept with their dates: the
rules live in the pack yaml and stages and labels are chosen, not
dictated (9 Oct 2026); a people group with no members holding a stage is
fine, reopening finds the thread's bead first, `says` skips the hook's
own actor, the sandbox is the harness's own in the group's args and CI is
the outside reviewer's test runner (9 Oct 2026); triage is the hold, and
triage is not forced (9 Oct 2026).

### yardr-6yx8: yards that work together

Steps 1 and 2 of the design in the bead's body are built, and the link
under them is further along than the design assumed. The peer link
(yardr-8f44), mail between yards (yardr-xbyy), pairing with a short code
(yardr-j77z, yardr-mzx3), the receiving side (yardr-8a8w) and the sending
side with the peer runner (yardr-b35t) are all merged, with docs/peers.md
covering the whole. A stage worked by another yard is a group whose
runner is `peer` (internal/runner/peer.go), exactly the design's
recommended option: the flow file has no new syntax, `yardr flow show`
prints the group with `yard north` as members, and the map says
`peer north`. The kind `bead` covers the bead and its answer
(`PeerKinds` in internal/store/peers.go: ping, mail, file, bead).
`yardr peer intake` sets depot, flow, limit and hold time; beads arrive
held for a user to release, or a named crew member with
`release=<peer>`, or ready with `--auto`. The yard's own outcomes
`refused` and `undelivered` are reported as designed, nothing ends the
stage by a clock, and `bead show` carries the `at north` / `from
…@north` lines. Code crosses through a shared git remote as designed:
`contrib/peer/return-branch` pushes the receiver's branch, the sender's
daemon fetches it and checks the commit before applying the outcome
(docs/peers.md, "Code that comes back"). Around it: each yard tells the
other what its build understands (yardr-25e4), a held bead nobody
releases goes back as refused after the intake's time (yardr-0t0q), and a
name on a linked yard can be trusted as a person of this yard (yardr-tb6q,
YRD-23).

Not built: step 3 (while it runs: progress events, questions answered in
place, a bead sent again) and step 4 (review elsewhere: the sender's
branch goes out). Both are unfiled; no open bead names them. Step 3 was
reshaped after the design was decided: on 6 Oct 2026 the yardmaster took
the laptop mayor's input that a peer should be a source as Linear and
GitHub are, with the pair record shown through `yardr link` and
`yardr thread show` / `yardr reply` across the link, and the Gas City
mayor's points put anchors into step 2 and foreign-code handling into the
sender's review. The reshaping note says to check what yardr-8a8w and
yardr-b35t built for the record and events when step 3 is shaped, which
has not happened. The earlier idea the bead kept out of scope, following
another yard's events in general, is still wanted and still unbuilt.
mckean's decisions on this bead, kept with their dates: paired yards
pass beads through their flows, sender and receiver as decided, two
beads with a link (6 Oct 2026, "great! let's go with that"); every
recommended option of the design (6 Oct 2026, "so the recommendations
are great. let's do this"); relays allowed, trust per pair of yards, no
origin shown, no opt-in, with the loop guard kept by the yardmaster
(6 Oct 2026, 11:25); identities are not merged across yards, a scoped
grant on the link instead, mckean to decide when (8 Oct 2026). The
release rule a held arrival needs a user for was the mayor's, left for
mckean to overrule (6 Oct 2026).

### yardr-s48v: source adapters as modules outside the core

The direction is fixed and most of the road is built. mckean decided on
11 Oct 2026 that a source's logic is commands its declaration names and
an integration is a pack of its own (kind source), tracked in YRD-29;
the chain yardr-0a6f (no names in the core), yardr-embp (commands in a
declaration), yardr-x0bh (receive webhooks), yardr-t7ff (thread commands
in use) and yardr-as7b (integration pack) is merged. A declared source
(`yardr source add`, docs/integrations.md) today does eleven things a
compiled adapter does: receive, identify, link, reply and read a thread,
open a thread, set a status, move with a bead's stage, tell a user
outside the yard, say what a subscription still carries, and be read or
updated by a session or crew member through `yardr call` under grants.
Linear is packs/linear: linear.yaml plus the `normalize` and `thread`
commands. yardr-vgjp proved the stage map moving tracked issues through
it. yardr-00n0 is merged on main: the pack's source takes the name
`linear`, the declared source wins every lookup when the yard has it,
and `yardr pack apply linear` folds the yard's `linear-declared`
records into `linear`, with no upgrade path for other yards per mckean's
11 Oct 2026 decision that breaking changes are fine. The first step the
bead named, moving Linear onto the contract, is therefore done except
for yardr-ecan (open, backlog): removing the Go adapter
(internal/hooks/linear.go and the Linear halves of reply, open, state,
forward and issue handling).

What is left: yardr-ecan itself; GitHub out of the binary (its App login
in internal/hooks/githubapp.go, the dispatcher's rules in
internal/dispatch/github.go and ci.go, and places like
internal/dispatch/makes.go that answer only for `Source == "github"`,
per mckean's 10 Oct 2026 mail that yardr should not be aware of GitHub
directly, and his 11 Oct 2026 topic); and the bead's points 3 and 4,
grants and identity per action: a crew member's capabilities as the
operator-granted actions of a module, the module saying which scopes
each action needs. Those two still need a design. yardr-c98n (a hook
that went quiet) waits on this bead. mckean's decisions on this bead,
kept with their dates: integrations leave the binary, Linear included
(6 Oct 2026); yardr should not be aware of GitHub directly (10 Oct
2026); a source's logic is commands its declaration names, an
integration is a pack of its own (11 Oct 2026).

## 3. The proposal

For each of the four beads a note on that bead holds a proposed body,
checked against the code and docs as they are, saying what changed and
why. No bead is proposed for closure: each retains open design matter
that still wants mckean or the yardmaster, and each is the record of
decisions that must stay findable. What each proposed body does:

yardr-nxah keeps the later steps and drops the built one. The read-only
live view leaves the body for docs/web.md and the merged train; what
stays is acting from the browser (which needs the deferred
authentication decision first), read-only config pages (the JSON lists
exist, the pages do not), and the toggles with the customised state
(the command-line switch exists per role and per flow, the third state
does not). It keeps every decision of mckean's with its date and points
at yardr-7mkc and yardr-zyzp for the two live ends. It stays a design
bead in backlog, shaped with mckean before anything of it is built.

yardr-nx54 narrows to what is still open. The six questions are answered
by the contributors pack and the live hook tables, so the body becomes a
short record of how each is answered and where, keeping the 9 Oct 2026
pack decisions, with two open items: posting a review to the pull
request, which needs mckean's word on the yard's GitHub identity, and
reviewing with nobody watching, which needs the head placed before the
reviewer starts. It also asks whether depot aiquokka's hand-made
pr-review flow should move to the contributors `pr` flow now that the
pack ships.

yardr-6yx8 stays the record and gains a status. The decided design in
the body is history now; the proposed body keeps the decided points
with their dates, lists what landed step by step with the beads that
built it, and says what is left: step 3 as reshaped on 6 Oct 2026 (peer
as a source, shown through link and thread commands, to be shaped
against what yardr-8a8w and yardr-b35t built), step 4 (review
elsewhere), and the general following of another yard's events. It does
not reshape step 3; it says shaping is needed.

yardr-s48v becomes the checklist its notes already sketch. The proposed
body keeps the four-part contract and the 11 Oct 2026 decision, records
the merged chain and yardr-00n0, and lists what is left in order:
yardr-ecan (shaped, in backlog), GitHub out of the binary (named files,
no design needed beyond what the Linear move proved), and grants and
identity per action, which still needs a design, in one paragraph.

## 4. Left out

New designs. Each proposed body points at the design still needed in
one paragraph and stops: authentication for the web view's act step,
the yard's GitHub identity for posting reviews, the reshaped step 3 of
beads between yards, grants and identity per source action. No
follow-up bead is filed from this pass: the proposed bodies name them,
and the yardmaster files them when shaping. The bodies, stages and
links of the four beads are unchanged; only notes were added.

## 5. Open questions

For the operator, on this pass itself:

1. Adopt the four proposed bodies as written? The alternative is to
   adopt them with edits, or to close one and file its leftovers as
   new beads. Closing fits yardr-nx54 best, whose open items are the
   smallest, but it would strand the 9 Oct 2026 pack decisions from
   the record that explains them. Recommended: adopt all four.
2. Should depot aiquokka move from its hand-made pr-review flow to the
   contributors `pr` flow? Staying costs a second, narrower flow to
   keep; moving costs re-proving what already runs there. The proposed
   body on yardr-nx54 asks it; recommended: move it when the next
   outside pull request arrives, not before.
3. Is step 3 of beads between yards worth shaping now, or should it
   wait for real two-yard work to press on it? Shaping now costs a
   design round against code that is fresh; waiting risks shaping
   against code that has moved on. Recommended: wait for the press.

## 6. The beads it would become

None from this pass. It files no bead and moves none; the follow-ups
live as named items inside the four proposed bodies, for the
yardmaster to file when shaping: on yardr-nxah, the act step behind
authentication, read-only config pages, and the customised state; on
yardr-nx54, posting a review to its pull request and the unattended
head; on yardr-6yx8, the reshaped step 3 and step 4; on yardr-s48v,
GitHub out of the binary and the grants-and-identity design, behind
the already filed yardr-ecan.
