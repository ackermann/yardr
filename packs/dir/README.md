# yardr dir pack

The pack yardr ships for a depot that is a plain directory and no
repository ([docs/dir-depots.md](../../docs/dir-depots.md)): a folder of
documents, invoices, images. Beads are worked in the directory itself,
nothing is committed and nothing merges, so a reviewed bead is done.

`yardr depot init <depot>` sets a dir depot up from it, as the default
pack of a dir depot in a yard that names none, and
`yardr pack check dir --depot <depot>` shows what that would change. yardr
carries this pack in its binary, so the name is enough; `packs/dir`, with
the separator, is this directory in a checkout. The format and how a pack
maps to a yard are in [docs/packs.md](../../docs/packs.md).

- `pack.yaml`: two flows, the depot's and its trains', the two groups that
  work them, builders and reviewers, and the two a bead waits for a person
  in, `backlog` and `decisions`. No group lands anything, and there is no
  crew.
- `roles/`: the builder's and the reviewer's prompt, written for a
  directory: no branches, no commits, no diff; the builder's note names
  every file by path, and the check is the depot's test command if it has
  one.
- `flows/default/`, `flows/train/`: a stage file for every stage a bead or a
  train is worked or waits in.

The stages `backlog` and `decide` are routed to the people groups `backlog`
and `decisions`, in both flows: a bead there waits for whoever shapes or
answers it, who finds it in `yardr review` and moves it on by hand. The two
are the starter pack's, and the yard's rather than the depot's: an import
into a yard that has them changes nothing in them, and one into a yard
that lacks them asks who their members are. Where the roles say
`{{operator}}`, an import puts the yard's operator.

It is for dir depots only: `yardr depot init` refuses it on a git depot, as
it refuses the starter pack on a dir depot.
