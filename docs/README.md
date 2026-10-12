# yardr documentation

The [README](../README.md) has the quick start, the core concepts and the
commands. These pages go deeper. Yardr's [Principles](../README.md#principles) explain how a design or a change is judged.

| page | what it covers |
|---|---|
| [glossary.md](glossary.md) | yardr's words in one table, each with what it is, and the words yardr used before |
| [install.md](install.md) | getting the binary: a release archive (download, verify, put on PATH) or `go install`; macOS and a downloaded binary; what `yardr doctor` still asks for; herdr's server; a server without a desktop, in order; making a release |
| [agents.md](agents.md) | for an agent that installs or sets up yardr for a person: the loop over `yardr doctor`, what to ask the person, commands and their wrong guesses, what each command does without a terminal, where things are |
| [setup.md](setup.md) | `yardr setup`: the setup after the onboarding as forms: a depot, its way of working from a pack, a first bead, each page ending in the command it shows; changing over to the yardmaster and back |
| [doctor.md](doctor.md) | `yardr doctor`: what a yard has and what it lacks (programs, what runs, setup steps), every line it prints, its JSON; `yardr status`: what runs now and where it is reached |
| [configuration.md](configuration.md) | what lives in a repo's `.yardr/` and what lives in the store, precedence, where yardr keeps its own state; creating a yard, a setup that did not finish, starting over |
| [flows.md](flows.md) | bead lifecycles: named flows depots point at, repo flows on the base branch, which flow wins, type flows, terminal stages, outcomes and `bead done`, drawing a flow (`flow show --svg`, `flow draw`) |
| [line.md](line.md) | how a bead moves in practice, using depot yardr: backlog, yardmaster, builders, review, assembly |
| [dir-depots.md](dir-depots.md) | depots that are a plain directory, not a repository: the `dir` pack `depot init` sets them up from, their flows, a worked example (summarizing a folder of documents) |
| [packs.md](packs.md) | packs: a yard's flows, groups, roles, crew members and declared sources as one directory; `yardr pack check`, `apply` and `list`, how a pack maps to the yard, the import record and pruning |
| [groups.md](groups.md) | groups and runners (manual, exec, herdr, peer), what yardr knows of a harness kind (`yardr harness`), herdr setup |
| [review.md](review.md) | `yardr review`: working through beads waiting on humans |
| [crew.md](crew.md) | always-on sessions (a yardmaster): inbox, hooks, journal, prime, memories |
| [users.md](users.md) | users, the people of a yard: `yardr user`, the operator as one of them, mailing a user, a user's `yardr inbox` in its two parts (mail and notifications, acknowledged apart), being told outside the yard (`yardr user set <name> notify=<source>`: what is sent, when, what leaves the machine, a source that fails), a user's devices (`yardr user device add`, `list`, `rm`: a token shown once, who may add and remove one), a conversation read back (`yardr mail --with`), read state |
| [chat.md](chat.md) | `yardr chat`: the crew and a user's conversation with each in one terminal view; the states, where a sent mail stands, keys, what marks mail read, the `yard` row of notifications, the events it follows (`crew_status`, `mail_read`, `notifications_read`) |
| [timers.md](timers.md) | `yardr timer`: asking the yard to tell a crew member or a user something later, once or recurring, unless an awaited bead event came first (`--unless`); who sets one for whom, what a firing is, the limits and their settings, late firings, the events |
| [subscriptions.md](subscriptions.md) | subscriptions and notifications: hook sources, yardr's own bead events, `--done`, bead watch, groups, a user as subscriber (who may, the notification's text, the bound of 500), `--on` |
| [links.md](links.md) | `yardr link`: the outside issue (Linear, GitHub) a bead tracks or relates to; one tracking bead per issue, one tracked issue per source, `link move`; the one form of an issue's URL; a hook's map (`hook add --as --map`) that moves the tracked issue when its bead enters a stage |
| [integrations.md](integrations.md) | what an integration is: the capabilities a source may have (receive, identify, link, reply, open a thread, set its status, move with a bead's stage, tell a user outside the yard, say what a harness kind's subscription still carries), by source; a declared source, a service added by one YAML file (`yardr source`), and a call of one, a read or an update, by a session or a crew member the operator granted it (`yardr source grant`, `yardr call`); `yardr limits` and the command a source's `limits` key names, with aiquokka's file whole; setting one up step by step, with Linear from nothing to a merged bead closing its issue, and GitHub where it differs; what adding a service takes; what is missing |
| [pushover.md](pushover.md) | a push to a user's phone, from nothing: the two keys, the two files, the complete declaration of Pushover as a source, `yardr source add` and `set`, `yardr user set … notify=pushover`, a test message, what is sent and when; the same in a pack |
| [peers.md](peers.md) | `yardr peer`: linking two yards by their keys; pairing with a short code (`peer pair`) and what it rests on, invites, what a link is allowed to carry, push and pull, the signed requests, `peer ping`; mail between crew members of two yards (`yardr mail <crew member>@<peer>`, the `peers` setting, what is refused); what the signature proves and that it hides nothing |
| [api.md](api.md) | the socket API for a program that is not yardr (a chat window of its own): the socket and who may open it, a call by name and its reply, errors, what a server does with a caller of another build, the event stream, and each call and event a chat client needs with its fields and an example; what is stable and what is not for a client |
| [bdp.md](bdp.md) | serving the yard over the Bead Protocol |
| [web.md](web.md) | `yardr web serve`: the yard in a browser, read-only; its pages and their JSON, the layout, the look's tokens, adding a page |
| [otel.md](otel.md) | OpenTelemetry: `yardr otel export` (the event log as OTLP traces and logs), how beads, stages and sessions map to traces and spans, `TRACEPARENT` and `OTEL_RESOURCE_ATTRIBUTES` in every session, Claude Code and Codex, a Grafana example |

Pages mark features that are not built yet as **planned**.

## The code

```
internal/store      the bbolt store: records, indexes, revisions, event log and push hub
internal/dispatch   routing, claims, liveness, teardown (the only control loop)
internal/runner     manual, exec, herdr, peer
internal/bdp        the BDP Read server (protocol rules, selector, paging)
internal/web        the read-only web view (pages as HTML or JSON, embedded assets)
internal/rpc        the store API over the server's unix socket
internal/server     the store-owning server process; started on first use
internal/version    build revisions; notices a new binary installed over a running one
internal/cli        cobra commands
internal/config     where yardr keeps its state: --home, YARDR_HOME, ~/.yardr
internal/agents     the agent CLIs herdr can start: their models and unattended flags
internal/depotfile  reading a depot's file from its base, where a bead cannot have changed it
internal/hooks      webhook endpoints: each delivery becomes one hook event
internal/chat       the view behind yardr chat
internal/pack       reads a pack and plans what importing it would change
internal/flowdraw   draws a flow as a signalling diagram (SVG); go generate writes docs/flows from the starter pack
internal/yardkey    a yard's key pair and the signatures between linked yards
internal/pairing    linking two yards from a short code
internal/peer       unix-socket peer identity for the store and exec runner
internal/yards      messages between this yard and its peers
internal/otel       trace context for a bead's work; the event log as OTLP
main.go             the yardr binary
packs/starter       the starter pack: the default roles, flow and stage files, embedded in the binary
packs/dir           the pack for a depot that is a plain directory
docs/               the documentation (this page is its index)
conformance/bdp     runs BDP's own Read conformance suite against yardr bdp serve
contrib/assembly    merge-local, a reference assembly script for an exec group
contrib/peer        return-branch, the script that pushes the branch of a bead worked for a linked yard
contrib/release     builds the release archives and checksums.txt
contrib/isolated-yard  a throwaway yard and herdr server for tests that start agents (see its README)
```

`go test ./...` runs everything against throwaway store files;
`YARDR_TEST_RPC=1 go test ./...` runs the same suites through the unix-socket
client, so the wire is tested by the same assertions. The tests open
their stores without waiting for the disk on each commit
(`YARDR_STORE_NOSYNC`, which they set themselves): that wait was most of a
run's time. `YARDR_STORE_NOSYNC=0 go test ./...` keeps it.
