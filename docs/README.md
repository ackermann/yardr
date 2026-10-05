# yardr documentation

The [README](../README.md) has the quick start and a short tour. These pages
go deeper.

| page | what it covers |
|---|---|
| [install.md](install.md) | getting the binary: a release archive (download, verify, put on PATH) or `go install`; macOS and a downloaded binary; what `yardr doctor` still asks for; making a release |
| [setup.md](setup.md) | `yardr setup`: the setup after the onboarding as forms, one page per step, each ending in the command it shows; changing over to the mayor and back |
| [doctor.md](doctor.md) | `yardr doctor`: what a yard has and what it lacks (programs, what runs, setup steps), every line it prints, its JSON |
| [configuration.md](configuration.md) | what lives in a repo's `.yardr/` and what lives in the store, precedence, where yardr keeps its own state; creating a yard, a setup that did not finish, starting over |
| [flows.md](flows.md) | bead lifecycles: named flows rigs point at, repo flows on the base branch, which flow wins, type flows, terminal stages, outcomes and `bead done` |
| [pipeline.md](pipeline.md) | how a bead moves in practice, using rig yardr: backlog, mayor, builders, review, refinery |
| [dir-rigs.md](dir-rigs.md) | rigs that are a plain directory, not a repository: `rig init` for them, their flows, a worked example (summarizing a folder of documents) |
| [packs.md](packs.md) | packs: a yard's flows, groups, roles and residents as one directory; `yardr pack check`, `apply` and `list`, how a pack maps to the yard, the import record and pruning |
| [groups.md](groups.md) | groups and runners (manual, exec, herdr), herdr setup |
| [review.md](review.md) | `yardr review`: working through beads waiting on humans |
| [residents.md](residents.md) | always-on sessions (a mayor): inbox, hooks, journal, prime, memories |
| [subscriptions.md](subscriptions.md) | subscriptions and notifications: hook sources, yardr's own bead events, `--done`, bead watch, groups, `--on` |
| [links.md](links.md) | `yardr link`: the outside issue (Linear, GitHub) a bead tracks or relates to; one tracking bead per issue, one tracked issue per source, `link move`; the one form of an issue's URL |
| [bdp.md](bdp.md) | serving the yard over the Beads Protocol |
| [web.md](web.md) | `yardr web serve`: the yard in a browser, read-only; its pages and their JSON, the layout, the look's tokens, adding a page |
| [otel.md](otel.md) | OpenTelemetry: `yardr otel export` (the event log as OTLP traces and logs), how beads, stages and sessions map to traces and spans, `TRACEPARENT` and `OTEL_RESOURCE_ATTRIBUTES` in every session, Claude Code and Codex, a Grafana example |

Pages mark features that are not built yet as **planned**, with the bead
that builds them (e.g. *planned, yard-qexg*). The bead that ships a feature
flips its marker.
