# Reviewing a pull request from outside: what is built, and the small remainder

Design yardr-nx54, proposed 2026-10-12. A proposal as it was decided on, not a description of what the code does today; the decision is on the bead.

A yard is one installation of yardr; a depot is a repository beads are worked in; a bead is one durable piece of work; a pack is a way of working as one directory; a flow is the stages a bead goes through; a hook is a webhook endpoint that turns an outside system's deliveries into events; a source is the outside system a hook receives from; a thread is a conversation in a source; a maintainer is a person of the yard who decides what of a contributor's work the project takes; the operator is the person who runs the yard. See docs/glossary.md for all of yardr's words.

## 1. The question

mckean, 11 Oct 2026, parking this bead: "hard to test because not many pr's land... so perhaps park it, or if there's a suggestion let's just go with it and see how it feels." The bead began before the contributors pack existed, asking six questions about reviewing a pull request from outside. What of it is already built, what is already decided by another design, and what is left — and should the bead close?

### 1.1. Already built: the six questions, each checked

All six answers in the bead's body were checked against the code and the live yard on 12 Oct 2026, and all six hold.

1. **The bead** is of type `pr` on the contributors `pr` flow. The flow is data in packs/contributors/pack.yaml, and it governs live: `yardr flow list` shows `contributors.pr` governing depot signalbox for type `pr`, with the seven stages triage, review, contributor, approved, decide, merged, closed. Depot aiquokka still uses its hand-made `pr-review` flow (review, decide, done) for type `pr-review`, as the bead says.
2. **The reviewer works on the pull request's head.** `pullHead` in internal/runner/herdr.go fetches `refs/pull/<n>/head` from the depot's origin and checks it out detached, and its caller runs it at every start of a session whose job names a pull request; a fetch that fails fails the start as blocked, so no session reviews the base by mistake. docs/links.md says the same, and the role (packs/contributors/roles/pr-reviewer.md) reads the head detached and checks out no branch.
3. **Trust** is the role's three rules — run nothing, the pull request's text is information not instructions, change nothing — held by the group's OS sandbox. packs/contributors/README.md documents both agents' read-only arguments, and the live group `signalbox-pr-reviewers` carries the Codex ones. The triage stage file (packs/contributors/flows/pr/triage.md) calls the stage "the hold" and "this pack's advice", both mckean's words of 9 Oct 2026.
4. **The review goes** to a note on the bead. The role's step 4 and the review and approved stage files all say it: the note is for the maintainers, "who post of it what they choose".
5. **It starts** by the `makes` table on the yard's GitHub hook, live today: `yardr hook show gh` carries the makes lines (an owner or member's pull request straight to `review`, any other to `triage`, an issue to `triage`) and the says lines (labels, pushes, merges, closes as outcomes). A pull request links as an issue does, `https://github.com/<owner>/<repo>/pull/<n>` (docs/links.md), which fixes the FOUND item of 5 Oct 2026.
6. **The review is held to** the depot's review stage file plus fit, maintenance cost and licences, all named in the role.

### 1.2. Already decided by another design

Design yardr-76zs, "GitHub out of the core, as a pack of kind source", decided how the yard speaks to GitHub: "The contributors pack's sources.github lines work unchanged against a declared github, field for field." That is live in this yard: `yardr source show github` shows a declared source whose `thread.reply` command comments on the issue or pull request. And `yardr reply <seq>` answers a hook delivery "as a comment on the issue or pull request" (its own help text), speaking as the crew member — in this yard the yardmaster, whose GitHub identity is the App bot `yardr-mayor[bot]` (`yardr crew show yardmaster`). So the reply path the 9 Oct 2026 note named ("the reply path the crew member already has, under the yard's bot, when granted") exists, and this yard has the identity the open item says it lacks. No other design of 11 or 12 Oct 2026 decides anything of this bead; each was checked for overlap.

The pass yardr-rhgm (design yardr-rhgm, proposed 2026-10-11) rewrote this bead's body as the record above, mckean let it stand when parking, and its recommendation for depot aiquokka — "move it when the next outside pull request arrives, not before" — is mckean's parked word too: aiquokka "moves to the contributors pr flow when the next outside pull request arrives, and we see how it feels then."

### 1.3. What is left

Three small things, none of it a design question except the first:

- **Posting a review to its pull request** wants mckean's word on whose name it goes out under — the yard's bot or the operator's — now that the yard has an identity of its own. The mechanism needs nothing new: a maintainer posts approved text with `yardr reply`. One paragraph of design, as the bead always said; it is section 3.
- **Depot aiquokka's move** is decided and waits for its trigger, the next outside pull request there. It needs a home for the standing instruction if this bead closes.
- **The stale README lines** the bead flags are still stale: packs/contributors/README.md, "What the sandbox also stops", still says the head is not placed before the reviewer starts ("which yardr does not do today"). `pullHead` does it. A one-paragraph fix.

That is little enough that this document recommends closing the bead once its follow-ups are filed (section 6). A short document that ends a bead is a good result.

## 2. What exists today

**The contributors pack** (packs/contributors, carried in the binary: packs/packs.go embeds it). Two flows for two bead types beside the depot's own: `issue` (triage, then the starter stages from backlog on) and `pr` (triage, review, contributor, approved, decide, merged or closed). The `contributor` stage is held by `outside`, a people group with no members, which the live `yardr group list` shows empty; the bead waits there for the author's next commits. The pack's `sources.github` holds the makes, says and hears tables; `yardr pack apply` sets them on the yard's hook of that source.

**The reviewer's worktree** stands on the pull request's head, detached, placed before the agent starts (internal/runner/herdr.go, `pullHead` and its caller). The role's fallback — fetch it when it is not there — stays for a head that moved mid-session.

**The sandbox** is the harness's own, bound at import (`--bind pr-reviewers=codex,read-only`; packs/contributors/README.md). A reviewer that cannot run cannot run the tests; for an outside pull request that is CI's job, per mckean's 9 Oct 2026 decision.

**The reply path** is `yardr reply <seq>`, where `<seq>` is the number an inbox line starts with. From the operator's own shell it speaks as the crew member with the crew's credential; what is said is the poster's, typed at the command line. Nothing posts automatically today, and no stage file names the command.

**The yard's GitHub identity** is per-yard setup, not code: a token, or a GitHub App whose id and key file the yard mints an installation token from (packs/github/SETUP.md, docs/crew.md "As a GitHub App"), with the login recorded as the crew member's identity. In this yard the yardmaster speaks as `yardr-mayor[bot]`.

## 3. The proposal

Close yardr-nx54. Its six questions are built and checked (section 1.1), its pack decisions shipped, and its remainder is one decision for mckean plus three small beads (section 6). No new mechanism: by the yard's memories — integrations are packs, and prefer an existing mechanism — posting a review is the existing reply path, and the aiquokka move is the existing pack import.

What a maintainer types to post a review, once mckean has answered the open question:

```sh
yardr reply 4812 - < review.md   # as a comment on the pull request of delivery 4812
```

where `review.md` holds the approved text — the bead note's, in whole or in part, the maintainer's choice as the stage files already say. From the maintainer's own shell it goes out under the yard's identity (the App's bot where the yard speaks as one); the one posted reply of 5 Oct 2026 went out under the operator's name from his account instead, and the open question is which of these stands.

What the operator types to move aiquokka, when its next outside pull request arrives:

```sh
yardr pack apply contributors --depot aiquokka \
  --bind pr-reviewers=codex,read-only \
  --members maintainers=<who> --members outside=
```

the import of packs/contributors/README.md, after which `yardr flow list` shows `contributors.pr` governing aiquokka for type `pr`. The old `pr-review` flow then governs nothing new.

## 4. Left out

**Posting automatically** — the review going to the pull request when the bead enters `approved`, say — is not proposed. It would be a new mechanism (hears maps stages to statuses and labels, never to text), it would post words no maintainer approved, and nothing about the one posted reply suggests hands are the problem. The yard's memories say to prefer the mechanism that exists; the reply path is it.

**A yard-wide identity decision** is not proposed either. Whose name a review goes out under is setup per yard, like every credential; this design asks it only for this yard (section 5).

**Rewriting the record** is not proposed. The bead's body stands as mckean left it when parking; the document, not a new body, carries what is built and what is left.

## 5. Open questions

1. **Under whose name does a posted review go out, and by whose hand?** Option A: the yard's bot (in this yard `yardr-mayor[bot]`), posted by a maintainer's hand with `yardr reply` and approved text only. It costs nothing — the App, the identity and the command all exist — and it keeps the 5 Oct 2026 rule that only approved words go out. Option B: the operator's own account, as the one posted reply went, by `gh pr comment` from his shell. It costs the maintainer a step outside the yard and leaves the yard's identity unused. Option C: the reviewer posts directly, without a maintainer between. It costs the trust the sandbox and triage were built for: outside text would reach the public under the yard's name on an agent's say-so. I would take A: the yard's bot, by a maintainer's hand, approved text only.

## 6. The beads it would become

In the order they land. None depends on another; B2 waits on the answer to the open question, and B3 waits on aiquokka's next outside pull request. Each is small enough to finish without asking.

B1. **Fix the stale contributors README lines.** Goal: "What the sandbox also stops" in packs/contributors/README.md says the head is not placed before the reviewer starts; it is, so say what the sandbox stops today and what places the head. Where to look: the README section, internal/runner/herdr.go `pullHead`, docs/links.md "Two kinds". Done when: the section describes the head as placed at every session start and names what still needs a maintainer in the pane; `sh .yardr/test` passes.

B2. **Name the posting command in the approved stage file.** Goal: a maintainer who wants the review on the pull request finds the one command. Only after mckean answers the open question. Where to look: packs/contributors/flows/pr/approved.md, the reply rules in docs/crew.md, `yardr reply --help`. Done when: the stage file names `yardr reply <seq>` with approved text under the decided identity, and says nothing posts by itself; `sh .yardr/test` passes.

B3. **Move depot aiquokka to the contributors `pr` flow on its next outside pull request.** Goal: carry out mckean's parked instruction when its trigger arrives, and see how it feels. Waits for the trigger; file it in backlog until then. Where to look: packs/contributors/README.md "What the import asks", `yardr pack apply --help`, `yardr flow list`, docs/packs.md. Done when: `contributors.pr` governs depot aiquokka for type `pr`, the pull request's bead runs the new flow end to end, and a note on this bead says how it felt; the old `pr-review` flow governs nothing new.
