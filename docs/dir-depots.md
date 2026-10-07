# Dir depots: work on a folder, not a repository

A depot does not have to be a git repository. A **dir depot** is a plain
directory: a folder of invoices, documents, images, whatever needs working
on. Beads are routed, worked, reviewed and closed as in any depot, with the
same groups, routes, inbox, `yardr review` and crew members; what a dir depot
drops is the branch machinery:

- Beads are worked in the directory itself: no worktree, no branch, no base,
  nothing to commit or merge. The group's `--limit` is the concurrency
  control, and a bead says which files or subfolders it touches.
- A bead's output is files in the directory. The worker's note names them
  by path; there is no new field for it.
- `.yardr/` (an optional check, and flows and roles where the depot keeps
  its own) is read from the directory as it is on disk, so an edit applies
  at once ([configuration.md](configuration.md#depot-kinds-git-and-dir)).
- There is no gate unless you add `.yardr/check`: `yardr depot check` passes
  with "no gate", since nothing lands anywhere.
- Trains only group their children, which work in the same directory; a
  train is reviewed as a whole once its children are closed.

## A worked example: summarize a folder of documents

A folder of invoices, one subfolder per month:

```
~/Documents/invoices/
  2025-08/  ...
  2025-09/  acme-0912.pdf  globex-0917.pdf  initech-0925.pdf
```

### 1. Register the folder and set it up

```sh
yardr depot add invoices ~/Documents/invoices --kind dir
yardr depot init invoices
```

`depot add` would detect a plain directory on its own; `--kind dir` says so
explicitly, and makes even a folder inside a checkout a dir depot. `depot
init` sets the depot up from the yard's default pack, which for a dir depot
is `dir`, the pack yardr carries for a plain directory
([packs.md](packs.md#the-default-pack)); `--pack <pack>` names another.

It prints the plan and asks which agent, model and permissions the two new
agent groups run with and, in a yard that lacks the groups `backlog` and
`decisions`, who their members are (`--bind builders=claude --bind
reviewers=claude --members backlog= --members decisions=jo --yes` answers
without a terminal; an empty list is anyone), then makes:

| what | where |
|---|---|
| groups `invoices-builders` and `invoices-reviewers`, agents with the pack's roles | the store; the roles under `<home>/packs/dir/roles/` |
| groups `backlog` and `decisions`, of people, where the yard lacks them | the store |
| the two flows below, as `dir.default` and `dir.train`, and the depot's pointers to them | the store |
| their [stage files](flows.md#stage-files-what-a-stage-is-for), one for each stage but `done`: what the stage asks before a bead leaves it (name your outputs, in `new`) and when each outcome applies | `<home>/flows/dir.default/`, `<home>/flows/dir.train/` |
| routes: `new` to the builders, `review` to the reviewers, a train's `open` and `review` to the reviewers, and `backlog` and `decide` of both flows to `backlog` and `decisions` | the store |

Nothing is written into the directory, and no gate: `.yardr/check` stays
yours to add. The roles are written for a directory: no branches, no
commits, name your files by path.

- Both groups are agents. For a person as the reviewer, point the route at a
  manual group afterwards (step 2); the next `yardr pack apply dir --depot
  invoices` would set it back.
- A new group works one bead at a time, which keeps one worker in the folder.
  The pack gives no limit, so one you set later stays through the next
  import. Raise it only when beads touch separate files.
- `backlog` and `decide` are routed, to the people groups `backlog` and
  `decisions`: a bead there waits to be moved by hand, is in `yardr review`
  (`-g decisions` for what waits for an answer), and the yard does not
  report it as unrouted. The two groups are the yard's, the ones the starter
  pack makes, with no depot in their names: a yard that has them keeps them
  as they are, members included, and its depots share them.
- The pack has no crew: its roles name no yardmaster, and a held bead waits
  for whoever runs the yard.

The starter pack is refused on a dir depot, and `dir` on a git depot: each
of the two declares the kind it is written for (`kind` in its pack.yaml,
[packs.md](packs.md#packyaml)).

The bead flow:

```
backlog -> new | decide
new -> review on done | decide on question | backlog
review -> done on done | new on changes | decide on question
decide -> backlog | new
done terminal
```

A worker finishes `new` with `yardr bead done <id>` (to `review`) or
`--outcome question` (to `decide`, for a person); review ends with `done`,
which closes the bead, or sends it back with `--outcome changes`.

The train flow is the same with `open` for `new`:

```
backlog -> open
open -> review on done | backlog
review -> done on done | open on changes | decide on question
decide -> open | backlog
done terminal
```

A train waits in `open` while its children are open (each child blocks it).
There is no train branch to integrate, so once the last child closes, the
train goes to the depot's reviewers, who look at what the children produced as
a whole. That takes a route you add (below). A train created without `-s`
starts in `backlog`, its flow's first stage; `-s open` skips that. `new` is
not one of a train's stages.

### 2. You as the reviewer

The pack's reviewers are agents. To review yourself, through `yardr review`,
send `review` to a manual group:

```sh
yardr group add invoices/review --runner manual
yardr route add --stage review --depot invoices --group invoices/review
```

A train's `open` stays with `invoices-reviewers`: an agent looks at the
children's outputs as a whole and ends with `done`, which hands the train to
`review`, and so to you.

### A dir depot's own flows and roles

A dir depot can keep its way of working in the directory instead, as files
under `.yardr/`, read from the directory as it is on disk:

| file | content |
|---|---|
| `.yardr/flows/default.flow` | the flow of the depot's beads |
| `.yardr/flows/type/train.flow` | the flow of its trains |
| `.yardr/flows/default/<stage>.md`, `type/train/<stage>.md` | their stage files |
| `.yardr/roles/<name>.md` | a role, for a group whose `prompt` is `roles/<name>.md` |

`default.flow` and `type/train.flow` govern the depot without a pointer,
once no store pointer stands in front of them: `yardr depot source invoices
flow depot` clears the one the pack's import set, and `--type train` the
trains' ([configuration.md](configuration.md#switching-one-of-them)). A
depot never set up from a pack has no pointer to clear, and its groups and
routes are made by hand:

```sh
yardr group add invoices/workers --runner herdr --limit 1 --set prompt=roles/builder.md
yardr route add --stage new --depot invoices --group invoices/workers
```

`yardr depot init` writes none of these files. A dir depot that got them
from an earlier yardr keeps them, and works from them as before.

### 3. A bead

```sh
yardr bead create "Summarize each PDF in 2025-09/ into 2025-09/summary.md" -d invoices -s new
```

What happens, with the daemon running (`yardr daemon`):

1. The bead is created in `new` (`-s new`; without it a bead starts in the
   flow's first stage, `backlog`, where the yardmaster shapes it and advances
   it); the route sends it to `invoices-builders`, and the herdr runner starts
   an agent with its working directory set to `~/Documents/invoices` itself.
   There is no worktree or branch. The brief is written under
   `~/Documents/invoices/.yardr/briefs/` and removed when the bead closes. The
   agent's environment has no `YARDR_BASE` or `YARDR_BRANCH`.
2. The agent reads the bead (`yardr prime --bead <id>`), reads the PDFs in
   `2025-09/`, writes `2025-09/summary.md`, and leaves a note naming what it
   produced:

   ```
   wrote 2025-09/summary.md: one section per invoice (acme-0912, globex-0917,
   initech-0925) with vendor, date, total and due date; read nothing outside
   2025-09/.
   ```

   It finishes with `yardr bead done <id>`, which moves the bead to `review`.
   If the bead is unclear (which currency for totals?), it notes the question
   and ends with `--outcome question` instead, and the bead waits in `decide`
   for you.
3. `review` is routed to the manual group of step 2, so the bead waits for
   you (left with the pack's `invoices-reviewers`, an agent reviews it and
   ends with the same outcomes):

   ```sh
   yardr review
   ```

   shows the bead, its last notes and, in place of a diff, where its outputs
   are (`no diff in dir depot invoices: the outputs are in ~/Documents/invoices,
   as the notes name them`). Open `2025-09/summary.md`, then pick:
   - **done**: the bead moves to `done`, a terminal stage, and closes.
   - **changes**: it goes back to `new` with your note, and a worker picks it
     up again.
   - **question**: it waits in `decide`.

   `yardr review list` shows the same queue without the form, and
   `yardr bead done <id> --outcome changes --note "..."` does the same from a
   script.

A dir depot that wants a bar before review can add an executable
`.yardr/check`, e.g. one that fails when a month folder with PDFs has no
`summary.md`. It runs in the directory, and the builder role runs it with
`yardr depot check invoices` before handing on.
