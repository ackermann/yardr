# Links to outside issues

A bead records the issue outside the yard it belongs to: a Linear issue, a
GitHub issue. A link is to an issue, not to a bead: edges between beads are
`yardr dep` ([pipeline.md](pipeline.md#epics)).

```sh
yardr link add yardr-k3f9 https://linear.app/acme/issue/YRD-12/web-view   # yardr-k3f9 tracks the issue
yardr link add yardr-k3f9 https://github.com/acme/webapp/issues/7 --relates
yardr link add yardr-k3f9 4812            # the issue of hook delivery 4812, as an inbox line numbers it
yardr link list yardr-k3f9                # the bead's links
yardr link list --issue https://linear.app/acme/issue/YRD-12             # the issue's beads, its tracker first
yardr link move https://linear.app/acme/issue/YRD-12 --to yardr-e2ab     # another bead tracks it from now on
yardr link rm yardr-k3f9 https://linear.app/acme/issue/YRD-12
yardr bead create "Web view shows links" --rig yardr --tracks https://linear.app/acme/issue/YRD-12
```

Linking only records. It calls neither Linear nor GitHub, and nothing yet
acts on a link: moving a tracked issue when its bead changes stage is
*planned, yardr-e9cv*.

## Two kinds

| kind | what it means |
|---|---|
| `tracks` | the bead's stage will move the issue. The default of `link add`; `--tracks` on `bead create` |
| `relates` | shown with the bead, never moves the issue. `--relates` on both |

Any bead type may track an issue: a task, an epic, a child of an epic. A
closed bead keeps its links.

## The rules

- **A bead tracks at most one issue per source.** One Linear issue and one
  GitHub issue together is fine. A second `tracks` link to the same source is
  refused, and the message names the issue the bead already tracks.
- **An issue has at most one tracking bead.** A second tracker is refused.
  The message names the bead that tracks the issue and both ways out:
  `yardr link move`, or link with `--relates`. A closed bead still counts.
- **Any number of `relates` links**, on a bead and on an issue.
- **A bead has one link per issue.** Adding the other kind to an issue the
  bead already links replaces the link, and the command says so. Adding the
  same kind again is refused.

`yardr link move <issue> --to <bead>` is for work that grew into an epic:
the epic tracks the issue in place of the bead that started it. It is one
write, so the issue is never without a tracker and never has two. The bead
that tracked it keeps a `relates` link. It refuses an issue no bead tracks
(use `link add`) and a target that already tracks another issue of that
source.

`bead create --tracks` and `--relates` make the links in the write that
creates the bead: a refused link creates no bead.

## One form per issue

`<issue>` is the issue's URL, or the seq of a hook delivery from its thread
(the number an inbox line starts with, as `yardr reply` takes it). The
source's adapter turns either into the issue's one fixed form, which is what
the link stores:

| source | fixed form | left out |
|---|---|---|
| Linear | `https://linear.app/<workspace>/issue/<KEY-n>` | the title slug (it changes with the title), anything after it, query and fragment |
| GitHub | `https://github.com/<owner>/<repo>/issues/<n>` | anything after the number, query and fragment |

The workspace, owner and repo are lowercased and Linear's key uppercased:
both sources match them in any case. Two links are to the same issue exactly
when these forms are equal, and a delivery's thread is matched to a link the
same way, by its thread URL.

Refused: a URL on another host, a URL of the source that is no issue (a
Linear project, a GitHub discussion), and a GitHub pull request, though
GitHub numbers it with the issues. A delivery from a pull request's thread
or from a `token` hook names no issue either.

A link made from a delivery also records the thread's id in the source. A
link made from the URL takes that id from another link to the issue that has
it, and otherwise has none.

## Where links show

- `yardr bead show <id>` lists them with kind, URL and source;
  `--json` has them as `links`, each with `kind`, `source`, `url`, `thread`
  (when known), `created_at` and `created_by`.
- The event log: `linked` and `unlinked` on the bead, with `kind`, `source`,
  `url`, `thread` (when known) and the actor. A replaced link logs both, the
  `unlinked` with `replaced_by` and the `linked` with `replaced`; a move logs
  them on both beads, with `moved_to` and `moved_from`. Both events re-mint
  the bead's revision, as an edge does.
- BDP: every link is a Link to the issue's URL ([bdp.md](bdp.md#links-to-outside-issues)).
