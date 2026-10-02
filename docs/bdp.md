# BDP (Beads Protocol)

`yardr bdp serve` serves the yard over the [BDP](https://github.com/gastownhall/bdp)
Read profile, so any BDP client reads yardr's beads and dependency links over
HTTP:

```sh
yardr bdp serve --addr :8765 --scope https://beads.example/team/
```

- Beads are `<scope>beads/<handle>`; dependencies are Links whose source is
  the waiting bead and target its blocker, as in bd. The handle is the BDP
  id because it never changes and is unique within a yard (BDP ids are
  scoped), and it is what bd and humans use; the UUID is the `uuid`
  property. Listings follow BDP's canonical id order, not creation order.
- Bead properties are bd's JSON row (dependencies, counts, `created_by`, …)
  plus yardr's own fields; a held bead reads as `deferred`. Types use BDP's
  reference vocabulary (`https://work.example/types/task`, `…/blocks`) where
  names match, else `<scope>types/<name>`.
- Every real change mints an opaque revision (a no-op mints none), served
  as the ETag; listings page over one consistent snapshot.
- The revision is the compare-and-swap token a BDP write profile will take
  (`If-Match`). It is the bead's `revision` in `yardr bead show --json`, and
  the store already enforces it: `bead update`, `hold`/`unhold`, `advance`,
  `done` and `close` take `--revision <rev>` (in Go, `store.Expect.Revision`)
  and refuse with a `stale` conflict, changing nothing, when the bead has
  changed since that revision was read. Residents carry their own `revision`
  (`yardr resident show --json`) for `resident add`, `start`, `stop`,
  `restart` and `rm`.
- BDP's own conformance suite passes against it (31/31 runnable Read
  scenarios), and BDP's `bdp bd ready` client computes the same ready set
  from yardr as `yardr ready`. See [conformance/bdp](../conformance/bdp).
