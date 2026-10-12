# Yards that work together: what of the record is left

Design yardr-6yx8, proposed 2026-10-12. A proposal as it was decided on, not a description of what the code does today; the decision is on the bead.

A yard is one installation of yardr; a peer is another yard this one is linked with by its public key; a bead is one durable piece of work; the record bead is yardr-6yx8 itself, the bead that tracked the whole work of yards working together. docs/glossary.md has all of yardr's words. Below, "step 1" is a bead worked by another yard, "step 2" is its code coming back, "step 3" is what the two yards say while the bead is away, and "step 4" is review elsewhere, where the sender's branch goes out to be reviewed.

## 1. The question

mckean, 11 Oct 2026, on this record bead: "this one is important and I'd love to move forward on this, but it needs to be done with care." The record as rewritten by the pass yardr-rhgm stands, and the next step is designed before anything is built. The yardmaster then sent this bead to draft on 12 Oct 2026 as a stale bead (mckean, mail 18143: "we can let muse do some design work on older/stale beads"), with the instruction to read the designs of 11 and 12 Oct and the code first, and to write as the first section what of this bead is already built, what is already decided by another design, and what is left, recommending closing if little or nothing is left. So the question is that triage: what, if anything, of yards working together still wants designing?

The answer, in brief, before the evidence. Already built: the link, pairing, mail, cross-yard asks, steps 1 and 2, and the first half of step 3. Already decided by another design: the release rule (design yardr-evd6, decided 12 Oct 2026: the flow's first stage is the review, and the arrival hold with it is gone) and the whole of step 3 (the same design: the peer as a source). What is left: step 4, review elsewhere, which is unfiled and, this draft argues, unripe: it plugs into a seam of step 3 that is not built yet, and no real bead has crossed between two yards yet. And following another yard's events in general, unfiled and out of scope here by the bead's own body. This draft therefore recommends closing the record: one short document that ends the bead, with step 4 specified as the one design bead to file after the first real transfer.

The thread the record bead names (`yardr thread show 5307`) could not be read from this session, which has no Linear credential. This design goes by the dated notes on yardr-6yx8, which quote that thread, as the yardr-evd6 drafts did.

## 2. What exists today

### 2.1. Already built

The link between two yards is keys, an invite, signed messages, push or pull, and ping (internal/store/peers.go; internal/yards/receiver.go), with pairing by a short code both operators compare. Each yard says which message kinds its build understands, and `yardr peer show` prints it. Mail crosses between crew members (internal/store/peermail.go), with images, a roster of whom a peer may see and write to, and a scoped grant trusting a name as a person of this yard (`Peer.Trust` in internal/store/peertrust.go). An ask addressed to `name@peer` stands in that person's decisions in the other yard and its pick comes home (internal/store/peerasks.go). All of it is in docs/peers.md.

Steps 1 and 2 are built and merged. A stage is worked by another yard when its route leads to a group whose runner is `peer` (internal/runner/peer.go; `PeerBeadSend` in internal/store/peersend.go); the receiving yard takes the bead under an intake its operator set and returns the outcome (internal/store/peerbeads.go). Code comes back through the shared remote: `contrib/peer/return-branch` pushes the receiver's branch, and the sender's daemon fetches it and checks the commit (internal/store/peergit.go, internal/dispatch/peerfetch.go). A bead that nobody releases going back as refused after the intake's time was built and has since been taken out again with the hold it belonged to (section 2.2).

The first half of step 3 is built and merged: a bead from a linked yard arrives in the first stage of the intake's flow and nothing else holds it (yardr-9vnc); a yard tells the yard it works a bead for how the bead stands, by `bead-event` messages under the link word `thread` (yardr-ced1); and the source `peer`, whose events are deliveries, with the pair shown as a link row (yardr-bx1a). The glossary already says what the source is: "the outside system a hook receives from", with "`peer`, what a linked yard says of the beads this yard has at it". The second half is open work at P1: reading the other yard's bead (yardr-9mye, in review), answering on it (yardr-g514), a bead sent again (yardr-zajr), the doctor note (yardr-akbz), the two-yard trial (yardr-g4y6), and the first real bead with airy, run with mckean (yardr-emkn, in backlog).

### 2.2. Already decided by another design

**The release rule and step 3: design yardr-evd6, decided 12 Oct 2026.** mckean answered the yardmaster's ask with "A1" (mail of 12 Oct 2026, 17:10): every open question of the draft takes the draft's pick, all eleven. The record bead's first open item, the release rule, is therefore not open: the recorded user-only rule of 6 Oct 2026 and the build's crew-member middle level are both gone, replaced on mckean's own words of 12 Oct 2026 ("basically this could just be the first stage in the flow where foreign bead lands?"). The design's line, built by yardr-9vnc and now docs/peers.md ("The intake", "Who looks at a bead first"):

> "A bead from a peer then arrives as any bead is made: in the first stage of the intake's flow, not held, and routed as that stage is routed."

The intake in the code is now only a depot, a flow and a limit (`PeerIntake` in internal/store/peerbeads.go); `--auto`, `--hold-for` and `release=` are gone from the commands, the store and the docs. Step 3 as reshaped on 6 Oct 2026, the peer as a source as Linear and GitHub are, is the same design's proposal:

> "One built-in source, `peer`, beside `token` and `yardr`: the yards this one is linked with, heard about the beads this yard has away at them."

Its open beads above are building it. Step 3 needs no further design from this bead.

**Questions across yards: design yardr-h039, decided and fully built 12 Oct 2026.** All five of its beads are closed. Its line, now docs/peers.md ("Questions for a person of another yard"):

> "The writer addresses the ask to the person where they sit: `yardr bead ask yardr-n8ta "Move them now?" --to mckean@airy`."

Design yardr-evd6 confirmed its carrying for a question about a bead that is away (open question 5, decided A). Nothing of asking across yards is left to this bead.

**What a person is across yards: design yardr-r6jh, decided 12 Oct 2026.** It stays inside the recorded rule that identities are not merged across yards:

> "It was decided on 8 Oct 2026, and recorded as not to be reopened, that identities are not merged across yards. This design stays inside that: nothing below makes two records one."

Its builds, an ask for a user also sent where they work (yardr-sj1n), the home yard saying when a copied ask ended (yardr-1fxw), and `yardr bead ask --new` (yardr-s752), are open. The identity question the record bead carries is therefore someone else's open work, not this bead's.

**The edge, and what a source may declare: designs yardr-8wxe and yardr-76zs, decided 12 Oct and 11 Oct 2026.** The record's early thread asked how yards link and where security sits; yardr-8wxe decided, with the draft's pick on every open question, that a yard states what it lets in and "security at the edge is outside yardr (a private network, with Ory Kratos and Oathkeeper in front)". The link's per-kind, per-direction allow is that statement for yards. Design yardr-76zs answered what a source may declare, which design yardr-evd6 took as ground ("Design yardr-76zs answered what a source may declare, and it is built"). Read and bearing nothing on this bead: yardr-xpf8 (a footplate off the yard's machine), yardr-8y0f (telling asks on the phone), yardr-5t60, yardr-me78, and yardr-zyzp (withdrawn the day it was decided). Design yardr-0qs5 (a secret unseen by agents) has not landed and bears on nothing here.

The yard's memories hold for all of this: networking is "define what to allow", never a proxy built into yardr, which the link's allow words are; signalbox is a client of the web API, untouched by any of it; integrations are packs, which yardr-76zs made GitHub and Linear; and there are no upgrade paths, which is why bead 0 of design yardr-evd6 dropped the hold's fields and migrated nothing.

### 2.3. What is left

**Step 4, review elsewhere: the sender's branch goes out.** Unfiled: no design, no bead. Today the `bead` message names only where the work starts (`base`, `base_commit`, `remote`: internal/store/peersend.go, internal/store/peergit.go); the asking yard's own branch, with the commits to review, never goes out. Design yardr-evd6 left room for it in exactly one place, and that paragraph is the whole of the shaping step 4 has:

> "Step 4 is then one addition to the `bead` message (the asking yard's branch pushed to the shared remote and named, as `return-branch` names the working yard's) and the working yard's check of it, which mirrors step 2 in the other direction."

Two facts bound that design when it comes. First, its seam is not built: a bead sent again (yardr-zajr, open) is the bead that refuses, by `yard` with a note, when the asking yard's branch holds commits the other yard does not have, "and sending them out is step 4". Second, the intake is one depot and one flow per peer, and docs/peers.md ("The intake") says: "The sender picks neither: its bead's type does not choose a flow here." A yard that builds some beads for a peer and reviews others has one flow for both today; where review lands is the question step 4 must answer. The thread of a bead under review is settled already: design yardr-evd6 says "its events, reads and notes do not ask what kind of work the stage is."

**Following another yard's events in general.** Unfiled, and out of scope here by the bead's own body ("the earlier idea, still wanted later, out of scope here"). The source `peer` carries events about the beads two yards share and nothing else. Design yardr-evd6 left room for it in one paragraph:

> "Following a peer's events in general would be the same source with more envelope types beside `bead`, and it would need what this design avoids: a statement by each operator of which of their yard's events a peer may hear."

The envelope, the subscription and the inbox line would not change; the grant is the design that is missing. No bead asks for it, and no trial waits on it.

## 3. The proposal

Close the record bead, and file one design bead for step 4 after the first real transfer, not now. Nothing is built, and no page but this one changes.

The existing mechanism that carries the proposal is the design lane itself: a later design bead does step 4, shaped by the pointers of section 2.3 and by what the airy trial teaches. What is new is only the ending: the record's content now lives in landed pages, docs/peers.md and the design of yardr-evd6, and its remaining work is tracked by open P1 beads with owners. A standing open record past that point invites edits that rot; the trial bead yardr-emkn says what mckean says of the first transfer goes "on the record bead yardr-6yx8", and that line is repointed to the step-4 design bead when it is filed, or to yardr-evd6 if he says nothing that needs shaping.

Where a choice below is the operator's, the text says which open question of section 5 it is, and describes the option this draft would take.

Concretely, the person in `decide` closes this bead (open question 1, option A):

```sh
yardr bead close yardr-6yx8 --reason "record served: release rule and step 3 decided by yardr-evd6, step 3 building as P1s; step 4 to be designed after the airy trial (yardr-emkn)"
```

And after yardr-emkn lands, the yardmaster files the step-4 design bead of section 6 (open question 2, option A). No build bead is filed from this design: "the next step is designed before anything is built" is honoured by filing the design first.

## 4. Left out

**Designing step 4 now.** It would be designed against code that does not exist: its seam, the sent-again refusal of yardr-zajr, is an open bead, and the intake it must answer ("one flow per peer") has never held a real bead from another yard. No bead has crossed between two real yards yet; the link with airy carries `file`, `mail` and `ping`, not `bead`, and the first transfer waits in yardr-emkn to be run with mckean, who said of step 3 "let's wait for opus to build it and then test it together". A design written now would be reshaped by that trial. So this draft specifies the later bead instead of writing the design.

**Following another yard's events in general.** Out of scope here by the bead's own body, and wanted later with no one waiting. Its missing piece is a grant design, each operator stating which of their yard's events a peer may hear, which is a design of its own and not a paragraph of this one. It stays unfiled (open question 3, option A).

**Keeping the record open as a standing record.** The record's decided points are quoted in section 2.2 with their dates, and the designs they came from are landed pages that are not rewritten. What moves now moves in the open P1s. Keeping this bead open would keep a second place to update beside those beads and pages.

**Reopening anything decided.** The release rule, relays, the no-merge of identities, the peer as a source, asks as the carrying for questions: each is dated in section 2.2. None is reopened here.

## 5. Open questions

**1. Close the record now, or keep it until the airy trial lands?** Option A, close now, as proposed: the record's content is in landed pages, the remaining work is in owned P1 beads, and yardr-emkn's report line is repointed to the step-4 design bead or to yardr-evd6; it costs that the trial's words land on a bead about step 3 rather than on the record. Option B, keep this bead open until yardr-emkn lands and close it then: the trial reports where its bead says; it costs an open record with nothing to decide for the weeks of the build. I would take A: the repointing is one line in one bead.

**2. Step 4 designed after the airy trial, or now?** Option A, after yardr-emkn lands, as proposed: the design goes against built step 3 and real two-yard experience, and mckean's words from the trial are its shaping; it costs waiting, and that review elsewhere stays unshaped while it waits. Option B, designed now, in parallel with the step-3 build: review elsewhere is shaped sooner; it costs designing against the unbuilt seam of yardr-zajr and untested assumptions about the intake, with a likely second round after the trial. I would take A: "with care" is this.

**3. Following another yard's events in general: stays unfiled, or filed as a design question now?** Option A, stays unfiled, as proposed: it is out of scope here by the bead's own body, no one waits on it, and filing it now buys a second design queue behind step 4; it costs nothing but remembering, which this document and design yardr-evd6's paragraph do. Option B, filed now as a design bead for the grant: the later idea is tracked; it costs a design that cannot be drafted well before step 4 shows what crossing yards costs. I would take A.

## 6. The beads it would become

No bead is filed from this design now: the bead closes, and this document lands as its ending. One design bead is specified for later, to be filed after yardr-emkn lands; it depends on yardr-emkn and on the whole chain behind it (yardr-9mye, yardr-g514, yardr-zajr, yardr-akbz, yardr-g4y6). The yardmaster files it; the draft does not create beads.

**Design: review elsewhere: the sender's branch goes out (step 4).** Goal: the smallest careful design for a stage reviewed by a linked yard: what the asking yard pushes, what the `bead` message names, what the working yard checks before its flow begins, and where a bead to review lands when the intake names one flow per peer. Where to look: design yardr-evd6, section 4 ("Step 4, review elsewhere") and section 3.6 (the sent-again refusal this replaces); `PeerBeadSend` in internal/store/peersend.go and the `base`, `base_commit`, `remote` it sends; internal/store/peergit.go and internal/dispatch/peerfetch.go with `contrib/peer/return-branch`, which step 4 mirrors in the other direction; docs/peers.md ("The intake": one depot and one flow per peer, "The sender picks neither"); the notes of yardr-emkn for what the first real transfer taught. Done when: a design document of the design lane's shape is decided, saying which existing mechanism carries the branch out and what is new, with the trial being a review by airy of a branch of this yard; no code. Small enough that its builds, if any, are one bead each without asking: the message addition, the check, the intake answer, the trial.
