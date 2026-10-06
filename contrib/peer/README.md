# contrib/peer

One shell script, run as the `cmd` of an `exec` group, for a yard that works
beads for a linked yard and gives their code back. The whole of it is in
[docs/peers.md](../../docs/peers.md#code-that-comes-back).

**return-branch** pushes the branch of a bead that came from a linked yard.
It is routed for the last working stage of the flow such beads are taken
into, before the terminal stage that returns them:

```text
new -> push on done | returned on question | returned on failed
push -> returned on done | new on failed
returned terminal
```

```sh
install contrib/peer/return-branch ~/.yardr/scripts/    # from main
yardr group add web/return --runner exec --limit 1 \
  --set cmd='YARDR_PUSH=origin ~/.yardr/scripts/return-branch'
yardr route add --stage push --depot web --group web/return
```

It is also built into yardr: `yardr peer intake <peer> --depot <depot> --flow
<flow>` writes it to `~/.yardr/scripts/` when the depot is a repository, and
keeps one that is there. Install it from `main`, not a checkout, so a branch
cannot change how it is given back.

It pushes `yard/<this yard's bead>` to the remote `YARDR_PUSH` names, under
the same name: never the base, never forced, and it never merges. Then it
writes where the result is on the bead (`result.branch`, `result.commit`
and `result.remote` in its metadata, the remote's URL without credentials)
and reports `done`. The yard's return then names the branch, and the yard
that asked fetches it and checks the commit.

| what | outcome |
|---|---|
| pushed | `done`, with a note `pushed yard/<bead> at <sha> to <remote>` |
| the push failed, or the bead has no branch | `failed`, with git's words; held when the stage names no `failed` |
| no `YARDR_PUSH`, a remote the depot does not have, a branch that is not `yard/<bead>` | held, nothing pushed |
| a dir depot | exit 2: a flow for its beads needs no such stage |
