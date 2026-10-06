# yardr documentation

The [README](../README.md) has the quick start and a short tour. These pages
go deeper.

| page | what it covers |
|---|---|
| [install.md](install.md) | getting the binary: a release archive (download, verify, put on PATH) or `go install`; macOS and a downloaded binary; what `yardr doctor` still asks for; herdr's server; a server without a desktop, in order; making a release |
| [agents.md](agents.md) | for an agent that installs or sets up yardr for a person: the loop over `yardr doctor`, what to ask the person, commands and their wrong guesses, what each command does without a terminal, where things are |
| [setup.md](setup.md) | `yardr setup`: the setup after the onboarding as forms, one page per step, each ending in the command it shows; changing over to the mayor and back |
| [doctor.md](doctor.md) | `yardr doctor`: what a yard has and what it lacks (programs, what runs, setup steps), every line it prints, its JSON; `yardr status`: what runs now and where it is reached |
| [configuration.md](configuration.md) | what lives in a repo's `.yardr/` and what lives in the store, precedence, where yardr keeps its own state; creating a yard, a setup that did not finish, starting over |
| [flows.md](flows.md) | bead lifecycles: named flows rigs point at, repo flows on the base branch, which flow wins, type flows, terminal stages, outcomes and `bead done` |
| [pipeline.md](pipeline.md) | how a bead moves in practice, using rig yardr: backlog, mayor, builders, review, refinery |
| [dir-rigs.md](dir-rigs.md) | rigs that are a plain directory, not a repository: `rig init` for them, their flows, a worked example (summarizing a folder of documents) |
| [packs.md](packs.md) | packs: a yard's flows, groups, roles and residents as one directory; `yardr pack check`, `apply` and `list`, how a pack maps to the yard, the import record and pruning |
| [groups.md](groups.md) | groups and runners (manual, exec, herdr), herdr setup |
| [review.md](review.md) | `yardr review`: working through beads waiting on humans |
| [residents.md](residents.md) | always-on sessions (a mayor): inbox, hooks, journal, prime, memories |
| [users.md](users.md) | users, the people of a yard: `yardr user`, the operator as one of them, mailing a user, a user's `yardr inbox` in its two parts (mail and notifications, acknowledged apart), a conversation read back (`yardr mail --with`), read state |
| [chat.md](chat.md) | `yardr chat`: the residents and a user's conversation with each in one terminal view; the states, where a sent mail stands, keys, what marks mail read, the `yard` row of notifications, the events it follows (`resident_status`, `mail_read`, `notifications_read`) |
| [timers.md](timers.md) | `yardr timer`: asking the yard to tell a resident or a user something later, once or recurring, unless an awaited bead event came first (`--unless`); who sets one for whom, what a firing is, the limits and their settings, late firings, the events |
| [subscriptions.md](subscriptions.md) | subscriptions and notifications: hook sources, yardr's own bead events, `--done`, bead watch, groups, a user as subscriber (who may, the notification's text, the bound of 500), `--on` |
| [links.md](links.md) | `yardr link`: the outside issue (Linear, GitHub) a bead tracks or relates to; one tracking bead per issue, one tracked issue per source, `link move`; the one form of an issue's URL; a hook's map (`hook add --as --map`) that moves the tracked issue when its bead enters a stage |
| [peers.md](peers.md) | `yardr peer`: linking two yards by their keys; pairing with a short code (`peer pair`) and what it rests on, invites, what a link is allowed to carry, push and pull, the signed requests, `peer ping`; mail between residents of two yards (`yardr mail <resident>@<peer>`, the `peers` setting, what is refused); what the signature proves and that it hides nothing |
| [bdp.md](bdp.md) | serving the yard over the Bead Protocol |
| [web.md](web.md) | `yardr web serve`: the yard in a browser, read-only; its pages and their JSON, the layout, the look's tokens, adding a page |
| [otel.md](otel.md) | OpenTelemetry: `yardr otel export` (the event log as OTLP traces and logs), how beads, stages and sessions map to traces and spans, `TRACEPARENT` and `OTEL_RESOURCE_ATTRIBUTES` in every session, Claude Code and Codex, a Grafana example |

Pages mark features that are not built yet as **planned**, with the bead
that builds them (e.g. *planned, yard-qexg*). The bead that ships a feature
flips its marker.
