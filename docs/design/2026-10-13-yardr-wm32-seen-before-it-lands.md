# Seen before it lands: a screenshot on the bead, read by the reviewer

Design yardr-wm32, proposed 2026-10-13. A proposal as it was decided on, not a description of what the code does today; the decision is on the bead.

A yard is one installation of yardr: its store, its crew and its depots. A depot is a git repository or a plain directory beads are worked in; a bead is one durable piece of work. A flow is the stages a bead passes through; a stage is one step of a flow; a route says which group works a stage; a group is who works it (a pool of agents, a script, people, or another yard); a runner is how a group's work starts (`herdr`, `exec`, `manual`, `peer`). An outcome is how a session says its stage ended (`done`, `changes`, `question`, `failed`); the flow picks the next stage. A note is evidence an agent leaves on a bead (`yardr bead note`). A blob is a file the yard keeps, an image for now, named by the SHA-256 of its bytes. A pack is a way of working as one directory; the operator is the person who runs the yard. See docs/glossary.md for all of yardr's words.

## 1. The question

The yardmaster, 13 Oct 2026, writing to mckean of several footplate landings under the subject "what nobody saw": a builder that changes something a person looks at has no display, tests pass, a reviewer reads the diff, and the first eyes on the result are mckean's, who found the faults himself on his laptop. mckean's answer (mail 25462): "we could think about solutions". The question is what the smallest such solution is: the least the yard can build that puts a picture, or a checked look, on the bead before it lands.

## 2. What exists today

### 2.1. The `look` flow: a preview and a person's eyes, for one type of one depot

`yardr flow show signalbox.look` prints the flow the bead names: `review -> preview on done`, `preview -> look on done | decide on failed`, `look -> approved | new`. It is pointed at by depot signalbox, type `look`: beads of that type travel it, and the rest of the depot follows the plain `default` flow from its repository (`yardr depot show signalbox`: `flow: default (repo@adc7a9e)`). So the yard already answers "for which beads it runs" twice over: a type selects a flow, and a route selects beads by stage, type, depot, label and priority (`yardr route add --help`).

The stage files live in the yard at `/Users/mckean/.yardr/flows/signalbox.look/<stage>.md`. `preview.md`: a script, not an agent, runs `deploy-dokku preview`, which pushes the bead's branch to the app `preview-<bead>` on the yard's Dokku host and notes the address on the bead (contrib/deploy/deploy-dokku; contrib/deploy/README.md, "A preview of a bead that has not landed"). `look.md`: "This stage is a person's. The last note of stage `preview` has the address: open it and try the change the bead describes. The code is already reviewed; this look is for what only a person sees." The person sends the bead to `approved`, or back to `new` with words for the builder, and the bead comes through review and the preview again at the same address.

What it costs is a person's minutes on every bead of the type, plus the Dokku host the preview runs on. What it covers is one depot's web pages: only signalbox has the preview wiring, and only beads someone typed as `look`.

### 2.2. Blobs: the yard keeps pictures already, but only mail may carry them

A blob is a PNG or a JPEG in the yard's home under `blobs/`, named by the SHA-256 of its bytes, kept once however often it is sent (internal/store/blobs.go, the comment above `BlobsDir`; `BlobTypes`; `PutBlob`). One blob may be 8 MiB unless the yard sets `blob_max_bytes`; one mail carries at most 8 (`MaxMailFiles`). `yardr mail --file <path>` puts the image and sends it with the mail, and where the mail is read the line ends with `1 image: <path>`: a crew member reads the file there, with what its harness reads any file with (`yardr mail --help`; `FilesLine`).

Two facts bound every proposal below. First, a blob no letter names is pruned: `pruneBlobs` keeps only blobs named by mail events, plus anything put within `BlobGrace` (one hour), since the mail that will name it is sent after it was put. A screenshot attached to anything but mail — a note, say — would be swept within the hour unless something counts it. Second, blobs are served to yardr's own clients: `GET /blobs/<hash>` on the socket server (internal/rpc/server.go) and `GET /user/blobs/<hash>` for users' devices (internal/rpc/user.go). The web view serves no blob route today.

Whether an agent sees the picture at that path is a property of its harness and model, and it is unproven for every one of them. `FilesLine` only formats a local blob path into the text the agent is shown; the harness files (harnesses/*.yaml) configure invocation flags, models and error patterns, and say nothing about images. The yard's words — "a crew member reads the file there", "what a crew member's harness reads a file by" (internal/dispatch/crew.go, the nudge) — assume the read, and no test proves a model performed it. This proposal therefore scopes the checked look to one reviewer it verifies first: the yard's review group `yardr-reviewers` (herdr runner, kind `codex`, model `gpt-6-luna`, prompt `roles/reviewer.md`, routed for `review` in yardr, footplate, signalbox, yardr.dev and aiquokka), proved by the trial bead 6.4 runs before the role text lands. Any reviewer whose harness cannot open the picture, or whose model cannot read images, does not block the bead: its role says to note "picture unchecked" with the reason and review code and words as today. The builder needs no vision — taking the shot is a shell command and confirming it is mechanical (the file exists, is non-empty, has the expected dimensions) — so only review groups carry the requirement.

### 2.3. Notes: text only, and read everywhere a look happens

A note is text with an author and a time (internal/store/graph.go, `Note`; `AddNote`). `yardr bead note <id> <text>` takes text alone (internal/cli/beads.go, the `note` command): there is no `--file`, and nothing in the store joins a note to a blob.

Notes are what the existing eyes read. `yardr prime` shows the bead with its notes to the agent working it; `yardr review` walks a person through beads showing "its notes and the diff"; the web view's bead page lists the notes oldest first (internal/web/beads.go, `BeadPage.Notes`); footplate's cards show the bead whole, notes included (footplate src/api.rs, "One bead whole: body, stage, labels, notes"; src/model.rs, "One note of the bead's body, as the expanded card shows it"). `yardr bead show` is the exception: it prints the header and the body, and no notes at all (internal/cli/beads.go, `printBeadFlow`). So a picture carried by a note would reach the builder's successors, the reviewer, the web view and footplate's cards as they stand, and would need one new line to be named by `bead show`.

### 2.4. Where a person would see pixels

The web view (`yardr web serve`, docs/web.md) is read-only: every route refuses any method but GET and HEAD. Its bead page renders notes as text in `<pre>` elements. It serves no images today, but a GET route for blob bytes fits its contract exactly, and an `<img>` tag on the bead page is then ordinary HTML.

Footplate renders mail images in conversations already: it fetches bytes by hash over the socket and shows them at a fixed height (footplate src/window.rs, `Picture`; src/api.rs, `blob`). Its cards — one per open ask on a bead, showing the bead's body and notes — render no images: pictures live in conversations, not on beads. Showing a note's picture on a card is new work in the footplate depot, against a bead payload that would have to name it (section 3.2).

A terminal shows no pixels. In `bead show` and `review` the picture can only be named: a hash, a path, a line saying what it shows. The reviewer's harness opens the path; the person opens it on their own machine.

### 2.5. Quota is windows, not tokens, and a session is its unit

The yard measures quota as each provider's usage windows, as the yard's limits source labels them (`yardr limits`; docs/groups.md, "A route with further groups and quota conditions"). A route may gate a group on them (`--group 'builders@Weekly<80,5h<90'`), and a bead no condition holds for waits. Nothing in the store or the dispatcher accounts tokens or dollars per bead or per session: the cost of a look, in quota, is the sessions it starts — each a claim on the windows — plus the image bytes inside sessions that already run. A label gates who pays: labels are the operator's convention with no meaning in yardr (`difficulty:`, `size:`, `topic:`), and routes match them (docs/groups.md, "Routing by estimate and quota"; `yardr route add --label`).

### 2.6. What takes the picture: a web page is easy, a footplate window is not

For a web page, the yard's machine is ready today. It is a Mac with Google Chrome installed, and headless Chrome draws without a display: on 13 Oct 2026 `Chrome --headless --screenshot=... --window-size=1280,800` wrote a 1280x800 PNG of a test page. The machine also carries `agent-browser` (v0.33.2, on PATH), a CLI for scripted browsing with `screenshot` and image `diff` commands. Either can shoot a page served from the worktree in seconds, inside a builder's session.

For footplate, which is gpui 0.2.2 exactly (footplate Cargo.toml), there is honestly no headless draw. gpui has a headless mode, but it is app logic with no windows: on Linux `HeadlessClient` reports no displays and creates no window (gpui-0.2.2 src/platform/linux/headless/client.rs), and on macOS headless skips launching the application (src/platform/mac/platform.rs, `MacPlatform::new(headless)`). No pixels come out of it; it exists for logic tests. Drawing a footplate window needs a display server and a GPU context, which the yard's headless Mac does not run for agents.

Three paths could still produce a footplate picture, none of them small. First, the Mac does have a logged-in console session (mckean, on `/dev/console`), so a footplate window opened there could be caught with `screencapture` — but that steals the operator's display from under him, races with whatever he is doing, and breaks the moment nobody is logged in. Second, mckean's laptop runs Arch with sway, where a headless output (`create_output HEADLESS-1`) plus `grim` can shoot a real window — but that needs the laptop online, the branch built on it, and a way back for the PNG. Third, offscreen rendering in gpui itself: a fork-level change to a crate pinned exactly because its API moves between releases. None is the smallest thing.

### 2.7. Packs carry whole flows, so a new stage is hand wiring first

A pack carries whole flows, groups, roles and crew (docs/packs.md); there is no fragment story for adding one stage to many flows (docs/design/2026-10-13-yardr-m0n8-deploy-stage-dokku.md, open question 5.4, took hand wiring for the same reason). The deploy design is also the precedent for what a picture stage is not: deploy is an `exec` script stage because pushing needs no judgement, while a look needs eyes. The `look` flow itself was wired by hand in the yard's store, per type, with no pack change.

## 3. The proposal

The smallest thing: the builder takes a screenshot and it rides the bead as a note that carries a blob; the existing reviewer opens it as part of review; persons see it where they already read notes. One new core mechanism — files on notes — carries it; blobs, routes, labels, the review stage and the preview stay as they are.

### 3.1. A label says the bead has something to see

A bead with a visible change carries a label, `eyes` (the name is open question 5.1's). Whoever moves the bead out of `backlog` — the yardmaster, by a line added to its estimating rubric in packs/starter/roles/yardmaster.md — applies it where the change shows: a page, a window, the web view. Beads without it pay nothing: no picture is taken, no reviewer opens one, no quota is spent beyond today.

A label, not a type and not a depot setting: a type selects a flow, and this proposal adds no flow and no stage; a depot setting would need a new setting and its plumbing, and would charge every bead of depots that mix visible and invisible work. Routes already match labels, so if a later stage wants only these beads, the filter exists (`--label eyes`).

### 3.2. The note carries the picture; the blob store carries the bytes

`yardr bead note` gains `--file <path>`, behaving as `yardr mail --file` does: the image is a PNG or a JPEG, put once by its bytes (`PutBlob`, same caps, same 8-per-note bound), and the note names it. The note's files are `BlobRef`s, the same shape mail carries, so footplate's `BlobRef` and the socket's `GET /blobs/<hash>` serve them unchanged. `pruneBlobs` counts note files beside mail files — without that line, section 2.2's sweeper takes every screenshot within the hour — and mail's grace still covers the put-then-note gap.

The builder's note says what the shot shows, in words, first: what was opened, at what size, what to look at, and the commit it was shot at (`git rev-parse --short HEAD`, the branch's tip after the last commit). The note stays readable where no pixels render (a terminal, `prime`), and the words are what the reviewer checks the picture against. Rendering a note anywhere a note is rendered appends the mail line, `1 image: <path>`, so agents open it the way section 2.2 names the path. `bead note --file` reads a local path on the machine where the command runs, as `mail --file` does (`putFiles` in internal/cli/crew.go reads the file with `os.ReadFile` and puts the bytes): attaching from anywhere but the yard's machine means copying the file there first.

### 3.3. The builder takes the shot; the reviewer reads it; no new session runs

For a web page, the builder shoots it inside its own session, always from a page served from the worktree, never from the Dokku preview URL. The preview cannot show the builder's code: the preview stage runs after review and pushes the bead's branch then (`preview.md`; contrib/deploy/README.md, "`deploy-dokku preview` pushes the bead's branch"), so at builder time the app either does not exist yet (the first cycle) or still shows the previous push (every later cycle). The builder starts the depot's own way of serving the page — signalbox's `npm run build` and a static server over `dist/`, or `npm run live` (scripts/serve.mjs) — on a free loopback port, shoots it with headless Chrome, or `agent-browser screenshot` where the page needs driving (a click, a scroll) before the shot, and stops the server by pid afterwards. The depot's builder role gains the recipe; a tiny reference script beside `contrib/assembly/merge-local` keeps the flags in one place (bead 6.5). The Dokku preview stays what it is: the address the person's `look` stage opens.

The shot is bound to the code it shows. The builder shoots after its last commit, after the test run, and the note names that commit's short SHA beside what was shot. The reviewer checks the named SHA against the branch tip: a match means the picture is the code under review; anything since that is visual needs a new shot and a new note before the bead can leave review, and so does any visual fix the reviewer commits forward itself. A stale picture — a valid-looking shot of older code — is then a mismatch the reviewer can see, not a trap.

For footplate, the builder takes no shot: section 2.6 says there is no honest headless draw on the yard's machine. A footplate bead with the label carries a picture only when a person put one there — mckean shooting his laptop's window with `grim`, copying the PNG to the yard's Mac, and attaching it there with the same `--file`, run on the Mac in his own pane, since `--file` reads a local path (section 3.2). Where no picture is attached, the reviewer checks code and words alone, exactly as today, and says so in its note. What would automate the footplate shot is open question 5.3, and it is not this build.

The checked look is the existing review stage, not a new one. The reviewer's role gains one paragraph: a bead with the label carries a screenshot in the builder's note; open the picture, check it against the bead's goal, the note's words and the named commit, and say what you saw in your own note — or, if the picture cannot be opened or your harness cannot read images, say "picture unchecked" with the reason and review as today. The reviewer is already a different agent than the builder, on a later stage, so the look is independent without a new session. Its quota cost is the image tokens inside sessions that already run — the reviewer's, to open the picture — against the provider windows of section 2.5, and nothing more: no new claim, no new condition, no route. The builder pays no vision cost: its confirm is mechanical (section 2.2).

### 3.4. What a person types and sees

The builder, in its session:

```sh
npm run build
python3 -m http.server 0 --directory dist > /tmp/serve.log 2>&1 & echo $! > /tmp/serve.pid
# /tmp/serve.log names the free loopback port it took; suppose 8137:
agent-browser open "http://127.0.0.1:8137/cards" && agent-browser screenshot shot.png
kill "$(cat /tmp/serve.pid)"
yardr bead note signalbox-ab12 "the cards page at 1280x800, scrolled to the third card, shot at 9bf658a" --file shot.png
```

The reviewer, in its session, reads the note through `prime` — `1 image: /Users/mckean/.yardr/blobs/<sha>` — opens the path, checks the named commit against the branch tip, and writes what it saw.

A person at the terminal:

```sh
yardr bead show signalbox-ab12
# ... the body, as today, then:
#   1 image: the cards page at 1280x800 (note 4)
```

(`bead show` prints no notes today, so the line is new; `review` shows the note with its image line as section 2.3 says it shows notes.)

A person in the browser opens the bead page and the note carries the picture inline, served by the web view's new GET blob route. A person in footplate opens the bead's expanded card and the note carries the picture as conversations carry mail images today.

## 4. Left out

- **A stage of its own that looks.** A new herdr group and stage — a looker session between review and landing, or a second reviewer that only reads pictures — costs one full session per labelled bead against quota, plus the hand wiring of section 2.7 in every flow that wants it, for independence the existing reviewer already gives. It is the bead after this one if review-with-a-picture proves too weak (section 6.7 states it so it can be taken without redesigning).
- **A person's look for every visual bead.** Extending the `look` flow beyond type `look` of signalbox costs a person's minutes per bead in every depot, and a preview host per depot that has none. The proposal keeps the person's look where it exists and spends pictures where it does not. Nothing here removes `look.md`'s stage.
- **Footplate automation.** Console capture on the yard's Mac, a capture loop on mckean's laptop, and offscreen gpui are all weighed in section 2.6 and all refused for the first build: fragile, far away, and fork-level respectively. Footplate pictures travel the same note files once a person takes them, by the copy-and-attach path of section 3.3.
- **Remote attach over the user API.** A device can already put an image to the yard (`PutBlob` over `/user/call`, as footplate's `put_blob` in src/api.rs does for mail drafts), but `AddNote` over that API takes text alone (internal/rpc/api.go), so a laptop PNG cannot reach a note without the copy step of section 3.3. Extending the call and footplate's card with an attach action is a later bead, taken if hand attachment proves routine enough to deserve it.
- **Video, traces and diffs.** A moving page wants a recording, a flaky layout wants a DOM snapshot, a restyle wants a before-and-after diff (`agent-browser diff screenshot` exists). Blobs carry PNG and JPEG; the note's words carry the rest. Each is a later bead if the still picture proves not enough.
- **Quota conditions for the look.** With no new session there is nothing to gate; section 2.5's conditions stay for routes that start agents. If section 6.7's stage is ever taken, its route takes a condition then.
- **Retention beyond mail's.** Note files are pruned exactly as mail files are: kept while named, swept when no note names them. A closed bead's screenshots stay while its notes do; nothing here ages or compresses them. Open question 5.4 asks whether that is the right bill.

## 5. Open questions

### 5.1. What marks a bead with something to see?

- **A label, applied by the yardmaster's rubric (`eyes`).** Costs a line in packs/starter/roles/yardmaster.md and the yardmaster's judgement per bead. Routes can match it later without touching the bead. A bead mislabelled either way either pays for an unneeded shot or lands unseen.
- **A type, with its own flow.** Costs a flow per depot that wants it and the hand wiring of section 2.7; it is the `signalbox.look` precedent (`type look`). It fits only if the look becomes a stage (section 6.7), since a type selects a flow and this build adds none.
- **A depot setting.** Costs a new setting, its plumbing, and a per-depot default; it charges every bead of mixed depots and needs an opt-out label anyway, which is the first option with more machinery.

Take the label.

### 5.2. Does the checked look live in review, or in a stage of its own?

- **In review, by role text (section 3.3).** Costs no session and no wiring. It trusts the reviewer to open the picture and say so; a reviewer that skims is caught only by its note, as a reviewer that skims a diff is today.
- **A looker stage with its own group.** Costs one session per labelled bead against the provider windows, the group's pool, and hand wiring in each flow. The look is then a gate of its own with its own note, and its route can carry a quota condition so labelled beads wait rather than burn.

Take review first. Section 6.7 holds the stage, but it is taken only on evidence and a decision: the operator, seeing labelled beads land with visual faults their reviewer notes claimed to have checked, or a run of "picture unchecked" notes showing reviewers cannot read images, decides review-with-a-picture is not enough and says so on a bead. Without that decision 6.7 stays unfiled.

### 5.3. What takes footplate's picture, and when?

- **Nothing automated; persons attach shots by hand (section 3.3).** Costs mckean's minutes per footplate bead he wants seen: the shot with `grim`, the copy to the yard's Mac, and one `bead note --file` run on the Mac. The plumbing is shared, so the day automation exists the pictures flow through it.
- **A capture trial on the yard Mac's console session.** Costs a bead to open footplate on the logged-in console and `screencapture` it. It steals the operator's display, races with his use, and fails headless; it proves only that the window draws, which was never in doubt.
- **A capture loop on the laptop.** Costs a bead plus a standing arrangement: the Arch laptop builds the branch, draws it on a headless sway output, shoots it with `grim`, and posts the PNG to the yard. It is the only honest automation, and it is a second machine in the critical path of every footplate bead.
- **Offscreen rendering in gpui.** Costs a fork of a fast-moving crate pinned exactly for that reason. It is the right answer at the wrong size.

Take hand attachment, and revisit the laptop loop once note pictures are routine.

### 5.4. How long is a screenshot kept?

- **As mail: while named (section 3.2).** Costs disk under `blobs/` proportional to labelled beads, about a megabyte a shot at window sizes. A closed bead's pictures stay readable, which is also what review archaeology wants.
- **Swept with the bead's close.** Costs a prune rule joining notes to bead state. It bounds the disk and loses the evidence the reviewer checked — the note's words stay, the pixels go.
- **Downscaled after landing.** Costs a converter in prune's path and a second blob shape. It keeps thumbnails for archaeology at a fraction of the bytes, and new machinery for it.

Take mail's rule: kept while named.

### 5.5. Which persons' surfaces does the first build reach?

- **Web view and terminal (`bead show`, `review`), footplate cards next.** Costs two yardr beads (6.2, 6.3) before any footplate work; mckean reads pictures in the browser first.
- **All three at once.** Costs the footplate bead (6.6) in the same build, in another depot with its own review and landing; the design lands whole or not at all.
- **Terminal only.** Costs the least and shows the least: names and paths, no pixels for anyone.

Take web and terminal first, cards next.

## 6. The beads it would become

In the order they would land. Beads 6.1–6.5 land in yardr, 6.6 in footplate, 6.7 in yardr. None waits on any bead outside this list. 6.1 first: 6.2, 6.3, 6.4, 6.5 and 6.6 all wait on it, since every one of them uses or instructs `bead note --file` and the role text of 6.4 and the recipe of 6.5 would otherwise tell agents to run a flag that does not exist yet. 6.2–6.6 may land in any order once 6.1 has. 6.7 waits on 6.1 and 6.4 — the note files and the label with the roles that use it — and on the trigger of 5.2: the operator's decision, on a bead, that review-with-a-picture proved insufficient. Until that decision it stays unfiled. If 5.1 takes the type or the setting instead of the label, 6.4's scope changes as noted and nothing else moves.

### 6.1. Notes carry files: `bead note --file`, counted by prune, served over the socket

Goal: a note names images the way mail does, and the yard keeps them. (depot yardr)

Where to look: internal/store/blobs.go (`PutBlob`, `BlobRef`, `pruneBlobs`, `FilesLine`, `MailFiles`); internal/store/graph.go (`Note`, `AddNote`); internal/cli/beads.go (the `note` command); internal/cli/crew.go (the `--file` flag mail already takes, and its help); internal/rpc/server.go (`GET /blobs/<hash>`); footplate src/api.rs (`BlobRef`) for the shape to reuse.

Done when: `yardr bead note <id> <text> --file shot.png` (repeatable, same bounds as mail: PNG or JPEG, the yard's blob cap, at most 8) reads the local path on the yard's machine, puts the blob and the note names it; a note's files render as `N image(s): <path>` everywhere a note renders to an agent (`prime`, and the note text `review` shows); `pruneBlobs` keeps a blob a note names and sweeps one nothing names, with tests proving a note-only blob survives past `BlobGrace` and a denamed one goes; the socket serves note files by the existing blob route; `bead show --json` gains a field naming the notes' files (it carries no notes today); `go test` of the touched packages passes.

### 6.2. The web view shows note pictures on the bead page

Goal: a person opening `/beads/<id>` sees the screenshot inline. (depot yardr; waits on 6.1.)

Where to look: bead 6.1; internal/web/beads.go (`BeadPage.Notes`, the `<pre>` rendering); internal/web/server.go (routes); docs/web.md (read-only GET: the new route takes GET and HEAD only).

Done when: the view serves `GET /blobs/<hash>` with the blob's bytes and type, refusing a non-hash as the socket does; the bead page renders each note's files as images with alt text from the note's words; a blob the yard no longer holds renders as a line saying so, as footplate's `Picture::Not` does, never a broken icon; the JSON of the page names the files; `go test` of the touched packages passes.

### 6.3. The terminal names note pictures: one line in `bead show`

Goal: `bead show` says the bead has pictures, since it prints no notes. (depot yardr; waits on 6.1.)

Where to look: bead 6.1; internal/cli/beads.go (`printBeadFlow`).

Done when: `bead show` prints one line per note with files — the count, the note's sequence and author, and the first line of its words — and nothing for notes without; `review` needs no change (it shows the note, whose image line 6.1 renders) and the bead's note says so, proved by reading its code; `go test` of the touched packages passes.

### 6.4. The label and the two paragraphs: yardmaster marks, reviewer reads

Goal: labelled beads get shots, and reviewers open them — after proof the yard's reviewer can. (depot yardr; waits on 6.1.)

Where to look: packs/starter/roles/yardmaster.md ("Estimating a bead": the rubric the label joins) and packs/starter/roles/ (the reviewer's role); packs/dir/roles/reviewer.md (the directory depot's reviewer); docs/groups.md ("Routing by estimate and quota": labels as convention).

Done when: on a trial bead carrying a known image in a note file, the yard's configured review group (`yardr-reviewers`: codex, gpt-6-luna) describes the image back correctly in its note, proving the path in `prime` reaches the model as a picture, and the bead's note records the trial; the yardmaster's rubric applies the label of 5.1 to beads whose change shows, with three examples (a page, a window, the web view) and three that take none (a refactor, a doc, a flag); the reviewer roles say a labelled bead carries a screenshot in the builder's note, to open it, check it against the goal, the note's words and the named commit (section 3.3's binding), and write what was seen — or, for a footplate bead with no picture, to say the check was words alone, and, where the picture cannot be opened or the harness cannot read images, to say "picture unchecked" with the reason and review as today without blocking the bead; if 5.1 took the type or the setting, this bead carries that scope instead and its note says which; `go test` of the touched packages passes (packs have tests: packs/packs_test.go).

### 6.5. The builder's recipe: serve the worktree, shoot it, attach it to the note

Goal: one documented way to take the shot, working from a builder's pane. (depot yardr; waits on 6.1.)

Where to look: contrib/assembly/merge-local (the pattern for a reference script); contrib/deploy/README.md (why the preview URL is never the builder's shot: it pushes the branch after review); the headless Chrome flags and `agent-browser screenshot` proved in section 2.6; signalbox's README (`npm run build` into `dist/`, `npm run live` serving it) for the worktree serving the recipe names.

Done when: a reference script takes a URL and a file and writes a window-sized PNG, refusing a missing page and an unwritable file with its reason, with tests using a local page (no network, no Dokku) proving the shot and both refusals; the builder roles point at it and say to serve the page from the worktree on a free loopback port (stopped by pid after the shot), to shoot after the last commit and name its short SHA in the note (section 3.3's binding), and never to shoot the Dokku preview URL, with the reason in one line (it shows the previous push or nothing, since the preview stage runs after review); a footplate builder is told explicitly not to shoot headless and why (section 2.6 in two lines); `go test` of the touched packages passes.

### 6.6. Footplate's cards show note pictures

Goal: the bead's expanded card renders a note's images as conversations render mail's. (depot footplate; waits on 6.1.)

Where to look: bead 6.1 (the files on notes, as the bead payload names them); footplate src/window.rs (`Picture`: fetch by hash, fixed height, the `Not` line); src/model.rs (the expanded card's notes); src/api.rs (the bead fetch).

Done when: each note on the expanded card renders its images below its words, fetched by hash over the socket as mail images are; a hash the yard no longer holds shows the `Not` line, never a blank; a card with no note images looks as today; `cargo test` passes.

### 6.7. If review-with-a-picture is not enough: a looker stage of its own

Goal: the look becomes a gate with its own session, stated now so it can be taken later without redesigning. Only on the trigger of 5.2 — the operator's decision, on a bead, that review-with-a-picture proved insufficient — and not before. (depot yardr; waits on 6.1 and 6.4.)

Where to look: beads 6.1–6.5; docs/groups.md (quota conditions for the route); the `look.md` stage file of `signalbox.look` (the person's wording the looker's role answers).

Done when: a herdr group `lookers` (or one per depot, as the yard is then set up) is routed for a new stage after `review`, filtered `--label eyes` so unlabelled beads pass through untouched — the flow rules say how (a labelled bead enters, the rest skip, or every bead enters and the looker waves the unlabelled through with one line: the bead states which and why); the looker's role opens the note's picture against the bead's goal and either approves or returns the bead with concrete visual findings; the route carries a quota condition so labelled beads wait rather than burn when the windows are nearly used; `go test` of the touched packages passes.
