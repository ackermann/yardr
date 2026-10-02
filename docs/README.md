# yardr documentation

The [README](../README.md) has the quick start and a short tour. These pages
go deeper.

| page | what it covers |
|---|---|
| [configuration.md](configuration.md) | what lives in a repo's `.yardr/` and what lives in the store, precedence, where yardr keeps its own state |
| [flows.md](flows.md) | a rig's lifecycle: allowed transitions, type flows, terminal stages; planned named flows and outcomes |
| [pipeline.md](pipeline.md) | how a bead moves in practice, using rig yard: backlog, mayor, builders, review, refinery |
| [groups.md](groups.md) | groups and runners (manual, exec, herdr), herdr setup |
| [review.md](review.md) | `yardr review`: working through beads waiting on humans |
| [residents.md](residents.md) | always-on sessions (a mayor): inbox, hooks, journal, prime, memories |
| [bdp.md](bdp.md) | serving the yard over the Beads Protocol |

Pages mark features that are not built yet as **planned**, with the bead
that builds them (e.g. *planned, yard-9cza*). The bead that ships a feature
flips its marker.
