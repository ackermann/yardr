# yardr as a service: someone signs up and gets their yard

Design yardr-8wxe, proposed 2026-10-12. A proposal as it was decided on, not a description of what the code does today; the decision is on the bead.

A yard is one installation of yardr: its store, its crew and its depots. A depot is a git repository beads are worked in; a bead is one durable piece of work; the crew are the yard's always-on agents, and the yardmaster is the one that coordinates; a harness is the agent program herdr starts (claude, codex); a source is a service outside the yard, declared by one file; a peer is another yard this one is linked with. See docs/glossary.md for all of yardr's words. "The service" below means the thing a customer signs up to, and "the service's operator" the person who runs it; "the customer" is the person who gets a yard.

## 1. The question

mckean, 11 Oct 2026: "separate story and this is bigger. what could yardr look like if i wanted to provide a saas? someone signs up they get their yard..." What could that look like, what would yardr itself have to change and what stays outside it, and what is a first step small enough to try? His premises, taken as given: "a user could use their own subscription" for model access; "herdr can run headless, we rely on it"; security at the edge is outside yardr (a private network, with Ory Kratos and Oathkeeper in front); and yardr stays one yard for one operator, so every customer gets an isolated yard of their own and yardr is not made multi-tenant.

## 2. What exists today

### 2.1. A yard is set up without a person, by flags

`yardr init --harness <kind> --operator <name> --start` creates a yard and starts its yardmaster with no form (docs/setup.md, "As a script"; docs/agents.md). `yardr depot add <name> --clone <url>` clones a repository into `<home>/depots/<name>`, and `yardr depot init <depot> --yes --bind builders=claude:opus --bind reviewers=codex --members decisions=<name>` gives it the starter pack's line: builders, reviewers, an assembly that lands, and the routes. `yardr doctor --json` names what is missing and the next command (`.next.next`), and docs/agents.md is the loop an agent follows over it. The first outside setup (the yardmaster's note on the bead, 12 Oct 2026) did exactly this on a friend's machine in about 20 minutes with no person; the one thing that cost time was an expired harness login.

`--home <dir>` makes any number of yards on one machine, each with its own store, socket and `hook_addr` (docs/install.md, "A yard somewhere else"). They share the machine's user, its herdr server and the harness logins in that user's home, so two yards on one machine are not isolated from each other.

### 2.2. A yard on a machine without a desktop

docs/install.md, "On a server (no desktop)", is the order: programs on a PATH a service sees, herdr's headless server (`herdr server`) as a systemd user unit, `loginctl enable-linger`, the harness logged in and proved with `yardr doctor --try`, `yardr init --start`, `yardr daemon install`. Two of the three things that keep a yard going have a command (`yardr daemon install`, `yardr web install`); herdr's server has none, and the page gives a unit file as an example that "yardr writes and tests neither". The page says of itself that it is written from one install (v0.1.0, Ubuntu, 5 Oct 2026) and that yardr's tests run on no machine without a desktop. After a reboot the daemon starts the server, finds the yardmaster and the sessions it had without their panes, and starts them again (docs/configuration.md, "After a reboot"). A newer binary installed over the old one is taken up by the server and daemon themselves (docs/configuration.md, "Upgrading"); the web view runs the build it was started on until it is started again.

### 2.3. The harness login

yardr starts a harness in a herdr pane and logs nobody in. What it knows of a login is in the kind's file (`yardr harness show claude`): the error lines that mean `not_logged_in`, raised as `session_harness_error`, and the login screen ("Select login method:", "Paste code here if prompted"), which is "told of at once, never typed into". The login is the harness's own, kept where its CLI keeps it in the home of the OS user; one login serves every agent of that kind on the machine. A group or a crew member may instead get its harness another account through an `env_file` or `env_command` (docs/groups.md, "Outbound credentials"; docs/crew.md): `KEY=VALUE` lines, mode 0600, joined to the agent's pane environment, never in the store. Reaching a pane takes a terminal: `herdr agent attach <agent>`.

### 2.4. Agents run as one OS user, with no sandbox of yardr's

A claude agent is started with `--permission-mode auto` (the kind's unattended flags; `bypass` and a read-only sandbox are presets a group may choose). Every agent, the daemon, the server and every script run as the one OS user. The documentation says so wherever it draws a line: "every agent runs as the same OS user and could read a 0600 file if it went looking" (docs/integrations.md), "a line, not a wall; it moves when yardr hosts more than one person" (docs/crew.md). The socket is the same: "a process that can open the socket is trusted as every party of the yard, and a call that takes an `actor` takes it at its word" (docs/api.md), and "yardr does not authenticate anyone" (docs/users.md). The builders run the depot's test command, which is the customer's code. Nothing in yardr limits what that code reaches on the machine or the network. A group's `--limit`, and `idle`, `max_age`, `stall` and `grace` (docs/configuration.md, "Session limits and outages") bound how many sessions run and how long.

### 2.5. The web view observes

`yardr web serve` (docs/web.md, internal/web) shows the feed, the flows, beads, sessions, crew and what waits for a person, live. Every route in internal/web/server.go is registered as `GET`, "so the mux refuses every other method itself: the view changes nothing". There is no authentication: it listens on loopback, or with `--unsafe` on any address, answering only to the names `--host` gives (421 otherwise, against DNS rebinding). `yardr web install` is loopback only, by an earlier decision (yardr-k3dy). `--ui <dir>` serves another front end's files (design yardr-zyzp, built). `GET /limits` answers what `yardr limits` says.

The acts a person needs exist as calls on the socket, used by footplate (a terminal interface) and `yardr chat`: `Mail`, `Conversation`, `UserInbox`, `UserAsks`, `Bead`, `AnswerAsk`, `DeferAsk`, `DeclineAsk`, `AddNote` (docs/api.md). `yardr review` moves a bead out of a stage that waits for a person (docs/review.md). The yardmaster does the setup "as a conversation" by mail and creates and shapes beads when asked (docs/setup.md). Acting from the browser is the first of the later steps kept on design bead yardr-nxah, which is in backlog and says "shape with mckean before any of it is built"; mckean's note there, 11 Oct 2026, is that the act step does not wait for a login built into yardr.

### 2.6. Secrets and grants

A source's credential is a variable in an env file the operator writes by hand and names with `yardr source set <name> env_file=`; `yardr source add`, `source set`, `source grant`, `crew set`, `peer allow` and `hook add` are the operator's, refused from an agent's pane. There is no way for a person to hand the yard a secret except a shell: that is design bead yardr-0qs5 (YRD-27), in backlog, not drafted. Its body says the line it would not move by itself: one OS user.

### 2.7. A customer's repository

Design yardr-76zs is built: GitHub is `packs/github`, a pack of kind source. Its SETUP.md is seven steps by hand: a webhook on the repository pointing at `https://<public host>/hooks/<hook name>`, a secret file, `yardr hook add`, a crew member's credential (a token, or a GitHub App's id and key file from which `contrib/github-mint/github-mint`, set as the source's `mint_command`, mints an installation token narrowed to one repository), the identity, the grants, the daemon's reads. The mint command is a setting only the operator sets, "any program", and "the private key stays out of the pack's script: only the operator's mint command reads it" (the design, 3.1.2). docs/integrations.md, gap 9: "yardr does not know its public address and makes no webhook in the service."

That credential is for the API (threads, pull requests, CI). The git transport is separate: `depot add --clone`, the runner's fetches and the assembly's push (`merge-local` pushes the base only when `YARDR_PUSH` names a remote, docs/line.md) use whatever git on the machine is configured with. yardr sets up no git credential.

### 2.8. Another yard works a stage

docs/peers.md, "Beads from a linked yard": a stage routed to a group whose runner is `peer` is worked by the linked yard, which makes a bead of its own, builds, pushes the branch to a git remote both yards use, and returns the outcome. "The yard that asked fetches the branch, checks it, and reviews, tests and lands it itself." A yard that cannot be reached from outside pulls its messages, so a laptop behind a router can be the working yard. Both sides need a clone of the same remote.

### 2.9. Limits and what is told outside

`yardr limits` asks a source with a `limits` key what a subscription still carries (docs/integrations.md); the command is the operator's and no pack brings one, and its answer is for "the login the provider reads on this machine". The daemon waits for a window that resets within six hours. A user is told outside the yard through a declared source (`yardr user set <name> notify=<source>`); packs exist for Pushover and Telegram (yardr-yrth). Events export as OpenTelemetry (`yardr otel export`), and `/sessions` lists what ran.

### 2.10. What a provider's terms say, read on 12 Oct 2026

Anthropic's page "Legal and compliance" for Claude Code (code.claude.com/docs/en/legal-and-compliance) has a section "Can customers offer Claude Code in their products?". As read on 12 Oct 2026: preinstalling or running Claude Code in a product or service, "e.g. in hosted sandboxes or other agent infrastructure", requires agreeing to the Commercial Terms of Service; the binary must not be modified and no built-in authentication method removed; and "customers may not pay for, resell, or intermediate Claude usage on their end users' behalf. Each end user must authenticate with their own Anthropic API key, Claude subscription plan credentials, or 3P inference provider credential". Under "Authentication and credential use": "developers may not collect, store, or intermediate Claude.ai credentials or session tokens — sign-in to a Claude account must complete through Anthropic's own flow", and the restrictions do not "prevent an end user from signing in to the unmodified Claude Code binary with their own Claude subscription, including where a platform hosts Claude Code". The same page says advertised Pro and Max limits "assume ordinary, individual usage". This is one reading of one page on one day, to be confirmed with Anthropic before anyone pays (open question 3). No other provider's terms were read: for Codex, Kimi and the rest the same question stands unanswered.

## 3. The proposal

### 3.1. The shapes, side by side

| | 1. A yard per customer, hosted whole | 2. A hosted yard, agents on the customer's machine | 3. Both, 2 first | 4. The yard at the customer's, the service only the edge |
|---|---|---|---|---|
| What runs at the service | one machine per customer: yardr, herdr, the harnesses, the clone, the builds and tests | per customer a yard with no agents: store, hooks, web view, the peer link | as 2, then as 1 | a relay: a public address for webhooks, a login in front of the customer's own web view |
| What the customer installs | nothing | yardr, herdr and a harness, then pairs the two yards | as 2 | everything, as today |
| Where the subscription is used | on the service's machine, through the harness's own login | on the customer's own machine | as 2, then as 1 | on the customer's own machine |
| Where the customer's code runs | the service's machine | the customer's machine for building; the hosted yard still fetches the branch and "reviews, tests and lands it itself" (docs/peers.md), so either it runs the tests after all, or every stage is routed away and it holds only the bead | as 2 | the customer's machine |
| yardr changes | acting from the browser; a server script that is tested; the rest is service | the same acting from the browser, and nothing for agents; the customer's side is today's self-hosting plus a pairing | both lists | none for a start |
| The service's own work | provisioning, the edge, a way to the login pane, the GitHub App with its broker and router, upgrades, backups, metering | provisioning (lighter), the edge, the GitHub App, the pairing reachable from outside | both | the relay and the edge |
| Cost to run, per customer | a machine that is on while the yard works; builds and tests are the weight, the model calls are the customer's | a small always-on process; no build weight | both | next to nothing |
| What it does not solve | the terms question of 2.10 has to hold | what hurt in the outside setup: the harness login, and keeping three things running after a reboot, stay with the customer, and a pairing is added | twice the building before anyone has a yard without a terminal | "someone signs up and gets their yard" is not what happens |

Shape 2's seam is real and built, but it places the hard half where the evidence says the pain is. The note on the bead says a hosted yard would take over the harness login and the three services after a reboot; shape 2 leaves both with the customer and adds the pairing and a remote both yards can push to. What it has for it is the terms: the subscription never leaves the customer's machine. Shape 4 is the cheapest and answers a different question (a phone view and webhooks for a yard one already runs); it is worth remembering as what a customer who will not host their code could be offered later. Shape 3 is two products.

The proposal is shape 1, and the choice is open question 1. Everything below is for shape 1.

### 3.2. Shape 1 in one picture

```text
browser ── Oathkeeper (asks Kratos who this is) ──┬─▶ customer A's machine: web view, on the private network only
                                                  └─▶ customer B's machine
GitHub ── the service's App: one webhook URL ─▶ router ──▶ A's machine: POST /hooks/gh, signed with A's own secret
A's machine ── mint command ─▶ token broker (holds the App's key) ─▶ a token for A's installation only

one customer's machine, one OS user:
  herdr server · yardr server · yardr daemon · yardr web serve · the harnesses, logged in by the customer
  ~/.yardr (store, packs, depots/<repo>) · nothing of another customer, nothing of the service but its own token for the broker
```

The wall between customers is the machine, since yardr has none inside it (2.4): one virtual machine per customer, not one home per customer on a shared machine, and not a container that shares a kernel with another customer's arbitrary code. Which kind of machine is open question 2.

### 3.3. What is the service's and not yardr's

Each of these is a program or a setting around a yard that runs yardr as it is. None is a yardr bead.

1. **Sign-up and the edge.** Kratos holds the accounts; Oathkeeper lets a logged-in customer through to their own machine's web view and to nothing else. The machine's view listens on the private network (`yardr web serve --unsafe --addr <private address>:8791 --host <the customer's name for it>`, as a unit of the service's own, since `yardr web install` is loopback only by decision), and no machine has a public address.
2. **Provisioning.** A machine image with yardr, herdr, git and the harnesses, and a script that runs 2.1 and 2.2: the herdr unit, linger, `yardr init --harness claude --operator <customer> --start`, `yardr daemon install`, the web view's unit, `yardr pack apply github`, the hook, the mint command, one harness kind bound to every group. The customer is the yard's operator user, the person the yardmaster brings decisions to; the commands that are "the operator's" are run by this script, at provisioning.
3. **The GitHub App, its token broker and its webhook router.** One App of the service's, installed by each customer on the repositories they choose. The App's private key can mint a token for every installation, so it is never on a customer's machine: the yard's `mint_command` is a small client that asks the broker, which knows which machine belongs to which installation and answers a token for that one, narrowed to the repository asked for. This is the mint as design yardr-76zs built it, with the operator's program being a client and not the key's reader. An App has one webhook address for all installations: the router verifies GitHub's signature with the App's secret, finds the customer by the installation, and posts the body to that yard's hook signed with that yard's own secret, in the header and form `packs/github` already verifies. The App's webhook secret is on no customer's machine either; a customer who read it could forge deliveries for another customer.
4. **git on the machine.** A git credential helper that asks the same broker, so `depot add --clone`, fetches and the assembly's push work without a key on the machine. The assembly is given `YARDR_PUSH=origin`, or the pull-request scripts of docs/line.md where the customer wants a pull request and not a push to the base.
5. **A way to the login pane.** The customer logs in to the harness in the harness's own screen (2.3, and the terms in 2.10 ask exactly that: no collecting of a subscription's token). Without a terminal of their own that takes a terminal in the browser, opened on `herdr agent attach <yard>-yardmaster` and reachable only through the edge. It is needed again whenever a login expires, which is the one thing that cost time in the outside setup. A customer who uses an API key needs no pane: the key is a line in an env file, and how it gets there without a shell is design yardr-0qs5's question.
6. **Being told.** The provisioning sets the customer's `notify` to a source that reaches them (the Pushover or Telegram pack, or one for the service's own mail), so "your harness is logged out" and "a decision waits" arrive outside the browser.
7. **Upgrades and backups.** A new yardr is put over the old on each machine; the server and daemon take it up, the web view's unit is restarted. The home is backed up: `yard.db`, the packs, `peer.key`.
8. **Metering and limits.** What a customer costs the service is machine time, not model calls. The service reads it from outside the yard (the machine's own accounting) and, where it wants to show work, from `/sessions` or `yardr otel export`. The customer's subscription windows show through `yardr limits` and `/limits` once the provisioning adds a limits source whose command reads the login on that machine. Bounds on compute are group settings the provisioning sets: `--limit`, `max_age`, `grace`.
9. **Stopping a machine that has nothing to do.** A yard survives a stop: "a restart is just another pass", and the daemon starts the crew again. A stopped machine receives no webhook and fires no timer, so the router would have to wake it. It is the lever on cost and is not part of a first step.

### 3.4. What changes in yardr

Three things, each general: a self-hosted yard gains from every one.

1. **The server setup as one tested script.** docs/install.md's server steps and its example unit for herdr become a script under `contrib/`, on the model of `contrib/isolated-yard`: from a fresh Linux machine with the programs on it to a yard that comes back after a reboot. The service's image calls it; so does anyone who puts a yard on a server. It is the answer to "yardr's tests run on no machine without a desktop".
2. **The web view acts, as one named user.** The view is started for a user (`yardr web serve --as <user>`), and then takes the writes that user may make, each an existing socket call: mail to a crew member and the conversation read back (`Mail`, `Conversation`), then the picks on an ask (`AnswerAsk`, `DeferAsk`, `DeclineAsk`) and the outcome of a bead that waits for a person (what `yardr review` does), each sent with the bead's revision. Without `--as` the view is as today: GET only. A write is refused unless it comes from the view's own origin, beside the Host check that is there. With mail to the yardmaster the customer has everything else the yard does, since the yardmaster creates, shapes and advances beads when asked; no page for creating a bead is proposed. This is step 1 of yardr-nxah cut into two slices; whether it is built from here is open question 6, and who the view acts as is open question 4.
3. **A person hands the yard a secret** (yardr-0qs5). Not designed here. This design gives it its hosted case as input: the first secret a hosted customer hands over is an API key for a harness or a token for a tracker, the person answers in a browser, and a subscription's login is not such a secret (2.10). Until it is built, a hosted customer who wants more than GitHub and one harness asks the service's operator.

What the yardmaster's list of missing things comes to, checked: acting from the browser is missing (2.5) and is item 2. Secrets and grants are missing (2.6) and are yardr-0qs5 plus, for the grants, the provisioning. Sign-up to a first merged bead with no terminal is scriptable today except the login pane and the acting (2.1, 2.3). Running arbitrary code safely is not something yardr does and is not proposed for it: the machine is the wall (2.4). The repository reaches the yard through the pack and mint that exist, with the App, broker, router and git helper as the service's (2.7). Limits exist as a source's answer; billing is the service's (2.9).

### 3.5. From sign-up to the first merged bead

What the customer does, once all of 3.3 and 3.4 exists:

1. Signs up (Kratos). The service provisions the machine; a few minutes later the customer's address shows the web view of an empty yard.
2. Installs the service's GitHub App on one repository, in GitHub's own page. The service clones it as a depot and sets the line up; the page then shows the depot and its flow.
3. Logs in to the harness: opens the login pane in the browser, chooses "subscription" or pastes an API key as the harness asks, and closes it. The feed, which showed the harness as not logged in, shows the yardmaster starting.
4. Reads the yardmaster's greeting in the view's conversation and answers: what the project is, what tests it. The yardmaster checks the test command with them.
5. Writes what they want built. The yardmaster makes a bead and advances it; the page shows a builder working, then a reviewer.
6. Picks an option where the yard asks (an ask in the needs list), or sends the bead on where a stage waits for a person.
7. Sees the bead merged, and the commit on the repository (or the pull request, where the line makes one).

Step 3 comes back whenever the login expires; the customer is told outside the browser (3.3, item 6).

### 3.6. What can go wrong for a customer's code and secrets

| what | what stands against it |
|---|---|
| Another customer's agent or test reads this customer's code or tokens | One machine per customer, no shared home, user or herdr server; machines do not reach each other on the private network |
| The service's App key or webhook secret leaks from a customer's machine and opens every customer | Neither is on any customer's machine: the broker mints, the router re-signs (3.3, item 3) |
| An agent, or code a test runs, sends the repository or a token somewhere | Nothing in yardr. The tokens on the machine are the customer's own, short-lived and narrowed to one repository; the harness login is the exception and is the customer's whole subscription. An outbound allow-list on the machine is the service's to add, at the cost of builds that fetch dependencies |
| Text from outside (an issue, a comment, a pull request) steers an agent | The contributors pack's triage stage, where a maintainer decides before anything of an outsider's is built or run; the mark "data, not instructions" on what a source or a peer sends. Both are lines, not walls |
| Someone else reaches the customer's web view and, once it acts, acts as them | The edge alone: the view has no login and, with `--as`, takes every request as that user. A mistake in the proxy's rules is a breach. The view on the private network only, and the origin check on writes |
| The service's operator reads the customer's code | Nothing technical: whoever runs the machine can read it. It is said to the customer plainly, and the service's access is by a named person, logged |
| The machine is lost | The repository is on GitHub; the store's backup holds the beads, notes and mail. A login is made again |
| The provider's terms forbid the hosted login, or change | An API key is the way the terms name without conditions; the customer's yard keeps working on one (open question 3) |
| A customer's builds run without end | Group limits and session ages set at provisioning; the machine's size is the hard bound |

### 3.7. The first step, with one outside person

No yardr change and no edge: one hosted yard, made by hand, for one person mckean knows.

- mckean makes one virtual machine on his private network and sets a yard up on it by the server steps (the script of 3.4, item 1, if it has landed, else by hand as the outside setup did by flags).
- The person gets an ssh login to that machine and nothing else. They log in to their own harness in the pane (`ssh -t host herdr agent attach <yard>-yardmaster`), give the repository (for the trial a fine-grained token of their own in the git configuration; the App comes later), talk to the yardmaster with `ssh -t host yardr chat`, and watch the web view through a forwarded port.
- It runs a week on a real repository of theirs.

What it answers, written down as a note on the trial's bead: how long from "here is your machine" to the first merged bead; each time the person needed the terminal and for what (this orders the beads of section 6 and the pieces of 3.3); how often the login expired; what the machine cost for the week and how much of the week it worked; whether a yard for a person who did not build it is a thing they come back to. The terms question (open question 3) is put to Anthropic in the same week, since everything after the trial depends on its answer.

## 4. Left out

- **A multi-tenant yardr**: users with passwords, a store for several customers, permissions between them. The premise excludes it, and 2.4 shows what it would take: every "line, not a wall" in the documentation would have to become a wall.
- **A sandbox inside yardr** for agents or tests. The harness's own permission modes are what a group chooses from; the machine is the wall. A second runner is excluded by the premise that herdr carries the agents.
- **yardr collecting a subscription's token** (a paste field for one). The terms as read forbid it for Claude, and the harness's own screen exists.
- **yardr making the webhook or knowing its public address** (docs/integrations.md, gap 9). With one App for the service, the router is where that knowledge sits.
- **A page for creating beads, configuring flows or adding sources in the browser.** The yardmaster takes the first by mail; the other two are yardr-nxah's later steps and yardr-0qs5.
- **A team in one yard**: several people of one customer, each acting as themselves. The view acts as one user; the header a proxy could carry is the option not taken in open question 4.
- **Stopping idle machines, an outbound allow-list, more than one harness kind per yard, trackers other than GitHub at sign-up.** Each is a later lever or a later pack; none is needed to learn whether a hosted yard is wanted.
- **Pricing, a brand, a landing page**, as the bead says.

## 5. Open questions

1. **Which shape.** (1) A yard per customer, hosted whole: the most service work (3.3) and a machine per customer, and the customer installs nothing. (2) A hosted yard with agents on the customer's machine: far less to run and no terms question, but the customer keeps the install, the login and the reboot, which is what hurt, and the hosted half is either a test runner after all or only a view. (3) Both, 2 first: two products. (4) The edge only, for yards people run themselves: nearly free, and not what was asked. I would take 1, and keep 4 in mind for the customer who will not host their code.

2. **What one customer's machine is.** A virtual machine each: the simplest wall and the dearest, always on. A micro virtual machine each (Firecracker and the like): the same wall, cheaper and stoppable, and a platform to learn and run. A container each on shared hosts: the cheapest, and a kernel shared between customers who run arbitrary code. I would take a plain virtual machine for the trial and for the first customers, and let the trial's cost figures say when the second is worth its work; I would not take the third.

3. **The subscription on a hosted machine.** Offer it, on the reading of 2.10 (the unmodified CLI, the customer's own login in the CLI's own screen, the service agreeing to the Commercial Terms and never touching the token): the premise holds, at the cost of needing a way to the pane and of depending on a page that can change. Offer an API key only: no dependence, no pane, and a customer pays per call beside their subscription. Offer both and confirm the reading with Anthropic first: one question to their sales contact, which the page itself invites. I would take the third. The page's "ordinary, individual usage" for Pro and Max limits is part of what to ask: a yard with a yardmaster and several builders at once may not be that. Other providers' terms are unread and are the same question each.

4. **Who the web view acts as.** One user named at start (`--as <user>`): a flag, no new trust, and it fits one yard for one operator; a second person of the same customer cannot act as themselves. A header the proxy sets, mapped to a user: several people, and yardr then trusts a header, which needs the rule for which addresses may send it. I would take the first; the second is the step for a team and can be added beside it.

5. **Where the service's code lives.** In a repository of its own (a new depot): the image, the provisioning, the broker, the router, the edge's rules; yardr keeps only what any yard on a server needs. Or under yardr's `contrib/`: one place, and yardr's repository then carries one service's business. I would take a repository of its own, with the server script alone in yardr.

6. **Whether the act step is built from this design.** Yes, as the two slices of 3.4, item 2, and yardr-nxah keeps its later steps (configuration pages, the customised state): the trial's findings and this design are the shaping that bead asked for. Or yardr-nxah gets its own design round first: slower, and one more look at the pages as a whole before they take writes. I would take the first, after the trial.

7. **Whether the first step is the trial of 3.7 as it stands**, with a terminal over ssh and nothing built. It costs a machine for a week and an afternoon, and it tells which of the pieces matter before any is built. The alternative is to build the edge and the browser acting first and try with a person who never sees a terminal: a truer test of the product, a month later. I would take the trial as it stands.

## 6. The beads it would become

For depot yardr, in the order they would land. B1 needs no decision. T is a person's, not a builder's. B2 and B3 wait for T and for open questions 4 and 6. The pieces of 3.3 are beads of the service's own depot once open question 5 is decided and are not listed.

**B1. A yard on a fresh Linux server, from one script, that comes back after a reboot.** Goal: `contrib/server-yard/` with a script that, on a machine that has yardr, herdr, git and a harness on PATH, does the server steps of docs/install.md: a PATH the services see, herdr's server as a systemd user unit, linger, `yardr init --harness <kind> --operator <name> --start`, `yardr daemon install`, and the web view as a unit; it prints what `yardr doctor` still lacks (the harness login) and changes nothing on a second run. A README beside it; docs/install.md's server section names the script and keeps the steps as its explanation. Where to look: docs/install.md ("On a server (no desktop)", "Keep it running after a reboot"), docs/configuration.md ("Login services", "After a reboot"), `contrib/isolated-yard/` for the shape of a contrib script with its test, `yardr daemon install --file-only`. Done when: on a fresh Ubuntu virtual machine the script ends with `yardr doctor` naming no fault but the harness login; after a reboot, with nobody logged in, `yardr status` shows server, daemon, web view and the yardmaster's start attempted; a test runs the script against stand-ins for herdr and the harness as the isolated yard does; `sh .yardr/test` passes. Depends on nothing.

**T. The trial of 3.7.** Goal: one outside person, one machine, one week, and the note of what was found. mckean's and the yardmaster's; depends on B1 only for convenience.

**B2. The web view, started for a user, sends mail to a crew member and shows the conversation.** Goal: `yardr web serve --as <user>` (refused when the name is no user of the yard); a page with the user's conversation with a crew member, live, and a form that sends mail through the socket's `Mail` as that user; the route takes POST only with `--as`, and only from the view's own origin; without `--as` every route is GET as today and a test holds that. `yardr status` and the view's start line say that it acts and as whom. Where to look: internal/web/server.go (routes, the Host check), docs/api.md (`Mail`, `Conversation`, `UserInbox`, the `mail` and `mail_read` events), internal/chat for what the terminal view shows, docs/web.md ("Adding a page", the content security policy that forbids inline scripts). Done when: in an isolated yard a mail typed in the page reaches the yardmaster's inbox as the named user and its answer appears without a reload; a POST from another origin, and any POST to a view without `--as`, is refused; docs/web.md says what the view takes and that the edge is what stands in front; `sh .yardr/test` passes. Depends on open questions 4 and 6.

**B3. The web view answers asks and sends on a bead that waits for a person.** Goal: on an ask in the needs list and on its bead's page, the options as buttons, with Later and Decline, through `AnswerAsk`, `DeferAsk` and `DeclineAsk`; on a bead in a stage that waits for the view's user, the outcomes its flow names as `yardr review` offers them, with a note; each write carries the revision the page was drawn from and a stale one is refused with the page redrawn. Where to look: internal/web/waits.go and the needs list (yardr-mkn6), docs/api.md (`UserAsks`, `Bead`, the three writes), internal/cli for what `yardr review` checks before a move, docs/review.md. Done when: in an isolated yard an ask is picked and a bead moved from `decide` in the browser, the notes are the named user's, and two tabs answering the same ask leave one answer and one refusal; `sh .yardr/test` passes. Depends on B2.

Proposed for beads that exist, not made here: yardr-0qs5 is drafted next after the trial, with 3.4, item 3 as its hosted case; yardr-nxah records that its step 1 was cut into B2 and B3 here, if open question 6 is answered so.
