# BDP conformance for yardr

`run.sh` runs [BDP](https://github.com/gastownhall/bdp)'s own Read-profile
conformance suite, black-box over HTTP, against `yardr bdp serve`:

```sh
git clone https://github.com/gastownhall/bdp ~/src/bdp
(cd ~/src/bdp && pnpm install --frozen-lockfile && pnpm build)
BDP_REPO=~/src/bdp conformance/bdp/run.sh
```

How it works:

1. `seed.py` builds a throwaway yard holding the graph BDP's bd-backed lane
   seeds (`seedBdWorkspace`): beads A–F, I, J, K, their `blocks` links and the
   E→F `related` link, with C and K closed and E deferred (held).
2. `yardr bdp serve` serves it under the Scope `https://scope.example/acme/`.
3. `make-fixture.py` derives the fixture from BDP's bd fixture
   (`read-bdpbd-v1.json`). Only implementation-specific literals change: bd's
   link ids become yard's, bd's content hashes become yard's revisions, and
   demo-a's property body and attribution become yard's. Every membership,
   direction, readiness and ordering expectation stays bd's. Yard's demo-a
   row and attributions are pinned in the script, not copied from the server
   under test; only run-time values (link ids and revisions, demo-a's uuid
   and timestamps) are read from it, each checked for its shape. The type
   inventory is bd's plus the four Types yard always has for links to
   outside issues (`relates`, `tracks`, `tracks-github`, `tracks-linear`),
   pinned in the script too.
4. `external-read.test.mts` runs the suite against the live server.

Journals and memories are not served, nor links touching them: they are not
work, and a bd client would count an open one as ready.

What yard does not claim, so the suite marks it not-applicable: the suite's
external-endpoint lane (it seeds a link to a URI that is no issue; yard's
only out-of-Scope endpoints are the issues of [links](../../docs/links.md),
tested in internal/bdp), pinned link endpoints, content-derived revisions,
aliases, owned links, disclosure. Rows that need BDP's in-process test hooks (lifecycle rows,
internal-fault injection) cannot run against any live server and are skipped.

Last run (BDP main 182f1fc, 2026-10-05): 31 of 31 runnable scenarios pass.
BDP's own `bdp bd ready --json` client, pointed at `yardr bdp serve`, returns
J, D, A: the same ready set bd itself does.
