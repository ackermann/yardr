# Dir rigs: work on a folder, not a repository

A rig does not have to be a git repository. A **dir rig** is a plain
directory: a folder of invoices, documents, images, whatever needs working
on. Beads are routed, worked, reviewed and closed as in any rig, with the
same groups, routes, inbox, `yardr review` and residents; what a dir rig
drops is the branch machinery:

- Beads are worked in the directory itself: no worktree, no branch, no base,
  nothing to commit or merge. The group's `--limit` is the concurrency
  control, and a bead says which files or subfolders it touches.
- A bead's output is files in the directory. The worker's note names them
  by path; there is no new field for it.
- `.yardr/` (flows, roles, an optional check) is read from the directory as
  it is on disk, so an edit applies at once ([configuration.md](configuration.md#rig-kinds-git-and-dir)).
- There is no gate unless you add `.yardr/check`: `yardr rig check` passes
  with "no gate", since nothing lands anywhere.
- Epics only group their children, which work in the same directory; an
  epic is reviewed as a whole once its children are closed.

## A worked example: summarize a folder of documents

A folder of invoices, one subfolder per month:

```
~/Documents/invoices/
  2025-08/  ...
  2025-09/  acme-0912.pdf  globex-0917.pdf  initech-0925.pdf
```

### 1. Register the folder and seed it

```sh
yardr rig add invoices ~/Documents/invoices --kind dir
yardr rig init invoices
```

`rig add` would detect a plain directory on its own; `--kind dir` says so
explicitly, and makes even a folder inside a checkout a dir rig. `rig init`
writes, never overwriting a file that is there:

| file | content |
|---|---|
| `.yardr/roles/builder.md`, `reviewer.md` | the generic roles, naming the yard's operator and mayor; each has a paragraph for dir rigs: no branches, no commits, name your outputs. No merger (nothing merges); the mayor's role only with `--with-mayor` |
| `.yardr/flows/default.flow` | the flow of the rig's beads, below |
| `.yardr/flows/type/epic.flow` | the flow of its epics, below |

and no `.yardr/check`. Nothing needs committing: the files apply as they are.

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

The epic flow is the same with `open` for `new`:

```
backlog -> open
open -> review on done | backlog
review -> done on done | open on changes | decide on question
decide -> open | backlog
done terminal
```

An epic waits in `open` while its children are open (each child blocks it).
There is no epic branch to integrate, so once the last child closes, the
epic goes to the rig's reviewers, who look at what the children produced as
a whole. That takes a route you add (below). Create epics with `-s open` or
`-s backlog`: `new` is not one of their stages.

Both flows are `default.flow` and `type/epic.flow` rather than named flows,
so they govern the rig without a pointer. A store pointer still wins
(`yardr rig flow invoices <flow>`); a rig pointer also overrides the
directory's type files ([flows.md](flows.md)).

### 2. Who does the work, and where it goes

```sh
yardr group add invoices/workers --runner herdr --limit 1 --set prompt=roles/builder.md
yardr group add invoices/review  --runner manual

yardr route add --stage new    --rig invoices --group invoices/workers
yardr route add --stage review --rig invoices --group invoices/review
```

`prompt=roles/builder.md` reads the rig's `.yardr/roles/builder.md` from the
directory. The review group is manual: you are the reviewer, through
`yardr review`. For an agent reviewer instead, make it a herdr group with
`--set prompt=roles/reviewer.md`.

With epics, route their `open` to the rig's reviewers. An agent reviewer
there looks at the children's outputs as a whole and ends with `done`, which
hands the epic to `review`, and so to you:

```sh
yardr group add invoices/reviewers --runner herdr --set prompt=roles/reviewer.md
yardr route add --stage open --type epic --rig invoices --group invoices/reviewers
```

Routing `open` to `invoices/review` instead works too; you then see the
epic twice, in `open` and in `review`.

`--limit 1` keeps one worker in the folder at a time. Raise it only when
beads touch separate files.

### 3. A bead

```sh
yardr bead create "Summarize each PDF in 2025-09/ into 2025-09/summary.md" -r invoices -s new
```

What happens, with the daemon running (`yardr daemon`):

1. The bead is created in `new` (`-s new`; without it a bead starts in the
   flow's first stage, `backlog`, where the mayor shapes it and advances it);
   the route sends it to `invoices/workers`,
   and the herdr runner starts an agent with its working directory set to
   `~/Documents/invoices` itself. There is no worktree or branch. The brief is
   written under `~/Documents/invoices/.yardr/briefs/` and removed when the
   bead closes. The agent's environment has no `YARDR_BASE` or
   `YARDR_BRANCH`.
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
3. `review` is routed to the manual group, so the bead waits for you:

   ```sh
   yardr review
   ```

   shows the bead, its last notes and, in place of a diff, where its outputs
   are (`no diff in dir rig invoices: the outputs are in ~/Documents/invoices,
   as the notes name them`). Open `2025-09/summary.md`, then pick:
   - **done**: the bead moves to `done`, a terminal stage, and closes.
   - **changes**: it goes back to `new` with your note, and a worker picks it
     up again.
   - **question**: it waits in `decide`.

   `yardr review list` shows the same queue without the form, and
   `yardr bead done <id> --outcome changes --note "..."` does the same from a
   script.

A dir rig that wants a bar before review can add an executable
`.yardr/check`, e.g. one that fails when a month folder with PDFs has no
`summary.md`. It runs in the directory, and the builder role runs it with
`yardr rig check invoices` before handing on.
